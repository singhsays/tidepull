package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"

	"github.com/singhsays/tidepull/internal/model"
	"github.com/singhsays/tidepull/internal/provider/dropbox/contenthash"
)

// Client talks to one Dropbox account over an authenticated HTTP
// client. Requests are retried with exponential backoff on transient
// failures.
type Client struct {
	hc          *http.Client
	apiBase     string
	contentBase string
	// folderID is the configured sync root: a path or ID, "" for the
	// account root. Dropbox identity is path-based — metadata carries
	// no parent IDs — so scope and ancestry resolve through paths.
	folderID string
	// scopePath / scopeRootID are the sync root's absolute path and ID,
	// resolved lazily on first use.
	scopePath   string
	scopeRootID string
	resolved    bool
	// paths caches absolute path -> folder ID.
	paths map[string]string
}

// New builds a client on top of an authenticated HTTP client for the
// account's sync root (a path or ID; "" for the account root). Empty
// base URLs select the production endpoints.
func New(hc *http.Client, folderID, apiBase, contentBase string) *Client {
	if apiBase == "" {
		apiBase = "https://api.dropboxapi.com"
	}
	if contentBase == "" {
		contentBase = "https://content.dropboxapi.com"
	}
	return &Client{
		hc:      withRetry(hc),
		apiBase: apiBase, contentBase: contentBase,
		folderID: folderID,
		paths:    map[string]string{},
	}
}

// withRetry wraps hc with go-retryablehttp: reads are retried up to 3
// times with exponential backoff on 429 and 5xx and connection errors.
func withRetry(hc *http.Client) *http.Client {
	rc := retryablehttp.NewClient()
	rc.RetryMax = 3
	rc.RetryWaitMin = 200 * time.Millisecond
	rc.RetryWaitMax = 2 * time.Second
	rc.Logger = nil
	rc.CheckRetry = func(_ context.Context, res *http.Response, err error) (bool, error) {
		if err != nil {
			return true, err
		}
		return res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500, nil
	}
	rc.HTTPClient = hc
	return rc.StandardClient()
}

// NewContentHash returns a hash.Hash computing Dropbox's content hash,
// used to verify downloaded content.
func (c *Client) NewContentHash() hash.Hash {
	return contenthash.New()
}

// Excluded reports whether the MIME type cannot be synced. Dropbox has
// no contentless file formats; only the account-configured exclusions
// apply.
func (c *Client) Excluded(mime string, extra []string) bool {
	return slices.Contains(extra, mime)
}

// CheckExport validates an account's export configuration. Dropbox has
// no exportable native formats; any configuration is invalid.
func (c *Client) CheckExport(cfg map[string]string) error {
	for name := range cfg {
		return fmt.Errorf("export: unknown type %q (supported: none)", name)
	}
	return nil
}

// ExportMime reports the MIME type to export the source type as when
// the account's export configuration enables it, "" when it does not.
// Dropbox has no exportable native formats.
func (c *Client) ExportMime(mime string, cfg map[string]string) (string, error) {
	return "", nil
}

// Export opens the file's content rendered as the target MIME type.
// Dropbox has no exportable native formats; the call always fails.
func (c *Client) Export(ctx context.Context, fileID, targetMime string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("export: dropbox has no exportable formats")
}

// rootSentinel is the identifier the engine uses for the account root.
const rootSentinel = "root"

// StartPageToken returns the cursor marking the current end of the
// folder's change stream.
func (c *Client) StartPageToken(ctx context.Context) (string, error) {
	if err := c.resolveScope(ctx); err != nil {
		return "", err
	}
	var out struct {
		Cursor string `json:"cursor"`
	}
	if err := c.rpc(ctx, "/2/files/list_folder/get_latest_cursor",
		map[string]string{"path": c.folderID}, &out); err != nil {
		return "", fmt.Errorf("start page token: %w", err)
	}
	return out.Cursor, nil
}

// Changes fetches one page of the change stream. It returns the
// entries, the cursor to resume from, and whether the stream caught up.
func (c *Client) Changes(ctx context.Context, cursor string) ([]*model.Change, string, bool, error) {
	if err := c.resolveScope(ctx); err != nil {
		return nil, "", false, err
	}
	var res struct {
		Entries []entry `json:"entries"`
		Cursor  string  `json:"cursor"`
		HasMore bool    `json:"has_more"`
	}
	if err := c.rpc(ctx, "/2/files/list_folder/continue",
		map[string]string{"cursor": cursor}, &res); err != nil {
		return nil, "", false, fmt.Errorf("changes list: %w", err)
	}
	changes, err := c.toChanges(res.Entries)
	if err != nil {
		return nil, "", false, err
	}
	// File and folder entries come before removals so rename detection
	// sees the pre-removal state.
	sortChanges(changes)
	return changes, res.Cursor, !res.HasMore, nil
}

// ListTree returns every file and folder under root (recursively).
func (c *Client) ListTree(ctx context.Context, root string) ([]*model.File, error) {
	if err := c.resolveScope(ctx); err != nil {
		return nil, err
	}
	var res struct {
		Entries []entry `json:"entries"`
		Cursor  string  `json:"cursor"`
		HasMore bool    `json:"has_more"`
	}
	if err := c.rpc(ctx, "/2/files/list_folder",
		map[string]any{"path": root, "recursive": true}, &res); err != nil {
		return nil, fmt.Errorf("files list under %s: %w", root, err)
	}
	// A parent's path is a strict prefix of its child's, so sorting by
	// path places folders before the files and subfolders inside them.
	// The recursive listing does not promise any order.
	slices.SortFunc(res.Entries, func(a, b entry) int {
		return strings.Compare(a.PathDisplay, b.PathDisplay)
	})
	changes, err := c.toChanges(res.Entries)
	if err != nil {
		return nil, err
	}
	files := make([]*model.File, 0, len(changes))
	for _, ch := range changes {
		if ch.File != nil {
			files = append(files, ch.File)
		}
	}
	return files, nil
}

// GetFile fetches one file's or folder's metadata by path or ID. The
// "root" sentinel maps to the account root, whose provider ID is empty.
func (c *Client) GetFile(ctx context.Context, fileID string) (*model.File, error) {
	if fileID == rootSentinel {
		return &model.File{ID: "", Folder: true}, nil
	}
	var e entry
	if err := c.rpc(ctx, "/2/files/get_metadata", map[string]string{"path": fileID}, &e); err != nil {
		return nil, fmt.Errorf("get file %s: %w", fileID, err)
	}
	if fileID == c.folderID {
		c.recordScope(e)
	}
	return c.toFile(e)
}

// Download opens the file's content stream (verified against the known
// content hash by the caller).
func (c *Client) Download(ctx context.Context, fileID string) (io.ReadCloser, error) {
	arg, err := json.Marshal(map[string]string{"path": fileID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.contentBase+"/2/files/download", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Dropbox-API-Arg", string(arg))
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", fileID, err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("download %s: status %d", fileID, resp.StatusCode)
	}
	return resp.Body, nil
}

// entry is one metadata object from the Dropbox API.
type entry struct {
	Tag            string `json:".tag"`
	ID             string `json:"id"`
	Name           string `json:"name"`
	PathDisplay    string `json:"path_display"`
	Size           int64  `json:"size"`
	ContentHash    string `json:"content_hash"`
	ServerModified string `json:"server_modified"`
}

// recordScope stores the sync root's path and ID from its metadata.
func (c *Client) recordScope(e entry) {
	if c.resolved {
		return
	}
	c.resolved = true
	c.scopeRootID = e.ID
	c.scopePath = e.PathDisplay
	if e.PathDisplay != "" {
		c.paths[e.PathDisplay] = e.ID
	}
}

// resolveScope resolves the sync root's path and ID once per cycle.
// The account root has no path and no ID.
func (c *Client) resolveScope(ctx context.Context) error {
	if c.resolved || c.folderID == "" || c.folderID == rootSentinel {
		c.resolved = true
		return nil
	}
	var e entry
	if err := c.rpc(ctx, "/2/files/get_metadata", map[string]string{"path": c.folderID}, &e); err != nil {
		return err
	}
	c.recordScope(e)
	return nil
}

func (c *Client) rpc(ctx context.Context, endpoint string, arg any, out any) error {
	body, err := json.Marshal(arg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return dropboxError(resp.StatusCode, data)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// dropboxError turns a Dropbox error response into an error, mapping
// the expired-cursor reset to the model's stale-token sentinel.
func dropboxError(status int, data []byte) error {
	var payload struct {
		Error struct {
			Tag string `json:".tag"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &payload)
	if payload.Error.Tag == "reset" {
		return fmt.Errorf("%w: cursor reset", model.ErrStaleChangeToken)
	}
	return fmt.Errorf("dropbox: status %d: %s", status, bytes.TrimSpace(data))
}

// toFile converts an API entry to the provider-neutral projection,
// resolving the parent folder's ID from the path.
func (c *Client) toFile(e entry) (*model.File, error) {
	parent, err := c.parentID(e.PathDisplay)
	if err != nil {
		return nil, err
	}
	return &model.File{
		ID:           e.ID,
		ParentID:     parent,
		Name:         e.Name,
		Folder:       e.Tag == "folder",
		Hash:         e.ContentHash,
		Size:         e.Size,
		ModifiedTime: e.ServerModified,
	}, nil
}

// toChanges converts one page of entries to change-stream entries.
func (c *Client) toChanges(entries []entry) ([]*model.Change, error) {
	changes := make([]*model.Change, 0, len(entries))
	for _, e := range entries {
		switch e.Tag {
		case "file", "folder":
			f, err := c.toFile(e)
			if err != nil {
				return nil, err
			}
			changes = append(changes, &model.Change{FileID: e.ID, File: f})
		case "deleted":
			changes = append(changes, &model.Change{
				Path:    c.relative(e.PathDisplay),
				Removed: true,
			})
		}
	}
	return changes, nil
}

// sortChanges orders file and folder entries before removals so rename
// detection sees the pre-removal state.
func sortChanges(changes []*model.Change) {
	var kept, removed []*model.Change
	for _, ch := range changes {
		if ch.Removed {
			removed = append(removed, ch)
		} else {
			kept = append(kept, ch)
		}
	}
	copy(changes, append(kept, removed...))
}

// parentID resolves the folder ID containing the given path: scope-root
// children report the scope root's ID (or "" for the account root),
// other parents come from the path cache or a metadata fetch.
func (c *Client) parentID(p string) (string, error) {
	dir := path.Dir(p)
	if dir == p {
		return "", nil
	}
	if dir == "/" {
		return c.scopeRootID, nil
	}
	if id, ok := c.paths[dir]; ok {
		return id, nil
	}
	var e entry
	if err := c.rpc(context.Background(), "/2/files/get_metadata",
		map[string]string{"path": dir}, &e); err != nil {
		return "", err
	}
	if e.ID != "" {
		c.paths[dir] = e.ID
	}
	return e.ID, nil
}

// relative strips the sync root's path from an absolute Dropbox path.
func (c *Client) relative(p string) string {
	if c.scopePath == "" || c.scopePath == "/" {
		return strings.TrimPrefix(p, "/")
	}
	return strings.TrimPrefix(strings.TrimPrefix(p, c.scopePath), "/")
}
