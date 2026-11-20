// Package google is the Google provider: its OAuth spec and a thin client
// over the Drive v3 REST API (change stream, tree listing, media download).
package google

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/singhsays/tidepull/internal/model"
)

// Client talks to one Drive account over an authenticated HTTP client.
type Client struct {
	svc *drive.Service
}

// New builds a client on top of an authenticated HTTP client. Requests
// are retried with exponential backoff on transient failures.
func New(ctx context.Context, hc *http.Client, opts ...option.ClientOption) (*Client, error) {
	svc, err := drive.NewService(ctx, append(opts, option.WithHTTPClient(withRetry(hc)))...)
	if err != nil {
		return nil, fmt.Errorf("drive service: %w", err)
	}
	return &Client{svc: svc}, nil
}

// withRetry wraps hc with go-retryablehttp: GETs retried up to 3 times
// with exponential backoff on 429/403/5xx and connection errors.
func withRetry(hc *http.Client) *http.Client {
	rc := retryablehttp.NewClient()
	rc.RetryMax = 3
	rc.RetryWaitMin = 100 * time.Millisecond
	rc.RetryWaitMax = 2 * time.Second
	rc.Logger = nil
	rc.CheckRetry = retryPolicy
	rc.HTTPClient = hc
	return rc.StandardClient()
}

// retryPolicy retries only body-less GETs on transient failures.
// Rate-limit 403s are retried; other 403s are permanent and fail fast.
func retryPolicy(_ context.Context, res *http.Response, err error) (bool, error) {
	method := http.MethodGet
	if res != nil && res.Request != nil {
		method = res.Request.Method
	}
	if err != nil {
		return method == http.MethodGet, err
	}
	if method != http.MethodGet {
		return false, nil
	}
	switch {
	case res.StatusCode == http.StatusTooManyRequests,
		res.StatusCode >= 500:
		return true, nil
	case res.StatusCode == http.StatusForbidden:
		return rateLimited(res), nil
	}
	return false, nil
}

// rateLimited reports whether a 403 response indicates a transient rate
// limit. The body is read and restored for the caller.
func rateLimited(res *http.Response) bool {
	data, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		return true
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	var payload struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return false
	}
	for _, e := range payload.Error.Errors {
		switch e.Reason {
		case "rateLimitExceeded", "userRateLimitExceeded", "dailyLimitExceeded":
			return true
		}
	}
	return false
}

// NewContentHash returns a hash.Hash computing the content hash used
// to verify downloaded content (the Drive MD5).
func (c *Client) NewContentHash() hash.Hash {
	return md5.New()
}

// fileFields is the metadata projection requested from the API.
const fileFields = "id,name,parents,mimeType,md5Checksum,size,modifiedTime,trashed"

// StartPageToken returns the token marking the current end of the change
// stream.
func (c *Client) StartPageToken(ctx context.Context) (string, error) {
	res, err := c.svc.Changes.GetStartPageToken().SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("start page token: %w", err)
	}
	return res.StartPageToken, nil
}

// Changes fetches one page of the change stream. It returns the entries,
// the token to resume from (the next page, or the new end-of-stream token
// on the last page), and whether the stream caught up.
func (c *Client) Changes(ctx context.Context, pageToken string) ([]*model.Change, string, bool, error) {
	call := c.svc.Changes.List(pageToken).
		SupportsAllDrives(true).
		IncludeItemsFromAllDrives(true).
		Fields(googleapi.Field("nextPageToken,newStartPageToken,changes(fileId,removed,file(" + fileFields + "))")).
		Context(ctx)
	res, err := call.Do()
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && (apiErr.Code == http.StatusNotFound || apiErr.Code == http.StatusBadRequest) {
			return nil, "", false, fmt.Errorf("changes list: %w: %w", model.ErrStaleChangeToken, err)
		}
		return nil, "", false, fmt.Errorf("changes list: %w", err)
	}
	changes := make([]*model.Change, 0, len(res.Changes))
	for _, ch := range res.Changes {
		c := &model.Change{FileID: ch.FileId, Removed: ch.Removed}
		if ch.File != nil {
			c.File = toFile(ch.File)
			c.Removed = c.Removed || ch.File.Trashed
		}
		changes = append(changes, c)
	}
	end := res.NextPageToken == ""
	next := res.NextPageToken
	if next == "" {
		next = res.NewStartPageToken
	}
	return changes, next, end, nil
}

// ListTree returns every file and folder under root (recursively), with
// trashed entries excluded.
func (c *Client) ListTree(ctx context.Context, root string) ([]*model.File, error) {
	var all []*model.File
	var walk func(parent string) error
	walk = func(parent string) error {
		pageToken := ""
		for {
			call := c.svc.Files.List().
				Q(fmt.Sprintf("'%s' in parents and trashed = false", parent)).
				Fields(googleapi.Field("nextPageToken,files(" + fileFields + ")")).
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true).
				Context(ctx)
			if pageToken != "" {
				call = call.PageToken(pageToken)
			}
			res, err := call.Do()
			if err != nil {
				return fmt.Errorf("files list under %s: %w", parent, err)
			}
			for _, f := range res.Files {
				file := toFile(f)
				all = append(all, file)
				if file.Folder {
					if err := walk(file.ID); err != nil {
						return err
					}
				}
			}
			pageToken = res.NextPageToken
			if pageToken == "" {
				return nil
			}
		}
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	return all, nil
}

// FolderMime is the MIME type of Drive folders.
const FolderMime = "application/vnd.google-apps.folder"

// nativeMimePrefix prefixes the MIME types of Google Docs Editors files
// (Docs, Sheets, Slides, Drawings, ...), which have no downloadable
// binary content.
const nativeMimePrefix = "application/vnd.google-apps."

// Excluded reports whether the MIME type cannot be synced: Google Docs
// Editors files (no downloadable binary content; downloading them fails
// permanently), plus the account-configured exclusions in extra.
func (c *Client) Excluded(mime string, extra []string) bool {
	if mime != "" && mime != FolderMime && strings.HasPrefix(mime, nativeMimePrefix) {
		return true
	}
	return slices.Contains(extra, mime)
}

// nativeTypes maps the type names used in export configuration to the
// Google-native MIME types.
var nativeTypes = map[string]string{
	"document":     "application/vnd.google-apps.document",
	"spreadsheet":  "application/vnd.google-apps.spreadsheet",
	"presentation": "application/vnd.google-apps.presentation",
	"drawing":      "application/vnd.google-apps.drawing",
}

// exportTargets lists the export targets each native type supports.
var exportTargets = map[string][]string{
	"application/vnd.google-apps.document": {
		"application/pdf",
		"application/rtf",
		"application/vnd.oasis.opendocument.text",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"text/html",
		"text/plain",
	},
	"application/vnd.google-apps.spreadsheet": {
		"application/pdf",
		"application/vnd.oasis.opendocument.spreadsheet",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"text/csv",
		"text/tab-separated-values",
	},
	"application/vnd.google-apps.presentation": {
		"application/pdf",
		"application/vnd.oasis.opendocument.presentation",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"image/jpeg",
		"image/png",
		"image/svg+xml",
	},
	"application/vnd.google-apps.drawing": {
		"application/pdf",
		"image/jpeg",
		"image/png",
		"image/svg+xml",
	},
}

// CheckExport validates an account's export configuration: every type
// must be known and every target must be exportable for it.
func (c *Client) CheckExport(cfg map[string]string) error {
	names := make([]string, 0, len(nativeTypes))
	for name := range nativeTypes {
		names = append(names, name)
	}
	slices.Sort(names)
	for name, target := range cfg {
		src, known := nativeTypes[name]
		if !known {
			return fmt.Errorf("export: unknown type %q (supported: %s)", name, strings.Join(names, ", "))
		}
		if !slices.Contains(exportTargets[src], target) {
			return fmt.Errorf("export: %s cannot be exported as %s", name, target)
		}
	}
	return nil
}

// ExportMime reports the MIME type to export the source type as when
// the account's export configuration enables it, "" when it does not.
func (c *Client) ExportMime(mime string, cfg map[string]string) (string, error) {
	for name, target := range cfg {
		if nativeTypes[name] == mime {
			return target, nil
		}
	}
	return "", nil
}

// Export opens the file's content rendered as the target MIME type.
// Exported content has no remote checksum; the caller records what it
// wrote instead.
func (c *Client) Export(ctx context.Context, fileID, targetMime string) (io.ReadCloser, error) {
	resp, err := c.svc.Files.Export(fileID, targetMime).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", fileID, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("export %s: status %d", fileID, resp.StatusCode)
	}
	return resp.Body, nil
}

// GetFile fetches one file's metadata.
func (c *Client) GetFile(ctx context.Context, fileID string) (*model.File, error) {
	f, err := c.svc.Files.Get(fileID).Fields(googleapi.Field(fileFields)).SupportsAllDrives(true).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("get file %s: %w", fileID, err)
	}
	return toFile(f), nil
}

// Download opens the file's content stream (verified against the known
// MD5 by the caller).
func (c *Client) Download(ctx context.Context, fileID string) (io.ReadCloser, error) {
	resp, err := c.svc.Files.Get(fileID).SupportsAllDrives(true).Context(ctx).Download()
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", fileID, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %s: status %d", fileID, resp.StatusCode)
	}
	return resp.Body, nil
}

// toFile converts an API file to the provider-neutral projection.
func toFile(f *drive.File) *model.File {
	parent := ""
	if len(f.Parents) > 0 {
		parent = f.Parents[0]
	}
	return &model.File{
		ID:           f.Id,
		ParentID:     parent,
		Name:         f.Name,
		MimeType:     f.MimeType,
		Folder:       f.MimeType == FolderMime,
		Hash:         f.Md5Checksum,
		Size:         f.Size,
		ModifiedTime: f.ModifiedTime,
	}
}
