// Package drivetest provides an in-process fake Drive v3 server: a
// change stream, tree listing, metadata and media, exercised through
// the real google provider client.
package drivetest

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/option"

	"github.com/singhsays/tidepull/internal/provider/google"
)

const (
	// RootID is the fake drive's root folder ID.
	RootID = "root-id"
	// DocMime is a Google-native document MIME type.
	DocMime = "application/vnd.google-apps.document"
)

// file is one file in the fake drive.
type file struct {
	id           string
	name         string
	parent       string
	mimeType     string
	content      []byte
	modifiedTime string
	trashed      bool
}

func (f *file) md5() string {
	sum := md5.Sum(f.content)
	return hex.EncodeToString(sum[:])
}

// changeRecord is one entry in the fake change stream.
type changeRecord struct {
	fileID  string
	removed bool
	file    *file
}

// Drive serves a minimal Drive v3 API over HTTP.
type Drive struct {
	mu      sync.Mutex
	clock   int
	files   map[string]*file
	changes []changeRecord
	srv     *httptest.Server
}

// baseTime anchors the fake modifiedTime stamps; each change bumps the
// clock by one second so ordering is testable.
var baseTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

func stamp(seq int) string {
	return baseTime.Add(time.Duration(seq) * time.Second).UTC().Format(time.RFC3339)
}

// New starts the fake server; it closes on test cleanup.
func New(t testing.TB) *Drive {
	t.Helper()
	d := &Drive{files: map[string]*file{
		RootID: {id: RootID, name: "My Drive", parent: "", mimeType: google.FolderMime},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /drive/v3/changes/startPageToken", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"startPageToken": %q}`, strconv.Itoa(len(d.changes)))
	})
	mux.HandleFunc("GET /drive/v3/changes", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		since, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
		var out []map[string]any
		for i := since; i < len(d.changes); i++ {
			ch := d.changes[i]
			entry := map[string]any{"fileId": ch.fileID, "removed": ch.removed}
			if ch.file != nil {
				entry["file"] = d.fileJSON(ch.file, i)
			}
			out = append(out, entry)
		}
		resp := map[string]any{"newStartPageToken": strconv.Itoa(len(d.changes))}
		if len(out) > 0 {
			resp["changes"] = out
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /drive/v3/files", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		parent := quotedID(r.URL.Query().Get("q"))
		var out []*file
		for _, f := range d.files {
			if f.parent == parent && !f.trashed {
				out = append(out, f)
			}
		}
		files := make([]map[string]any, 0, len(out))
		for _, f := range out {
			files = append(files, d.fileJSON(f, -1))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
	})
	mux.HandleFunc("GET /drive/v3/files/{id}", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		id := r.PathValue("id")
		if id == "root" {
			id = RootID
		}
		f, ok := d.files[id]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			_, _ = w.Write(f.content)
			return
		}
		_ = json.NewEncoder(w).Encode(d.fileJSON(f, -1))
	})
	d.srv = httptest.NewServer(mux)
	t.Cleanup(d.srv.Close)
	return d
}

// fileJSON renders one file's metadata. The seq argument is the change
// index providing its modifiedTime; -1 keeps the file's own stamp.
func (d *Drive) fileJSON(f *file, seq int) map[string]any {
	parents := []string{}
	if f.parent != "" {
		parents = append(parents, f.parent)
	}
	modified := f.modifiedTime
	if modified == "" {
		modified = stamp(0)
	}
	return map[string]any{
		"id":       f.id,
		"name":     f.name,
		"parents":  parents,
		"mimeType": f.mimeType,
		// The real API returns size as a quoted string.
		"size":         strconv.Itoa(len(f.content)),
		"md5Checksum":  f.md5(),
		"modifiedTime": modified,
		"trashed":      f.trashed,
	}
}

// quotedID extracts the first quoted ID from a files.list q expression.
func quotedID(q string) string {
	start := strings.Index(q, "'")
	if start < 0 {
		return ""
	}
	q = q[start+1:]
	end := strings.Index(q, "'")
	if end < 0 {
		return ""
	}
	return q[:end]
}

// AddFile registers a new file and records a change for it.
func (d *Drive) AddFile(id, name, parent, mime string, content []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := &file{id: id, name: name, parent: parent, mimeType: mime, content: content, modifiedTime: stamp(d.seq())}
	d.files[id] = f
	d.changes = append(d.changes, changeRecord{fileID: id, file: f})
}

// UpdateContent changes a file's content and records a change.
func (d *Drive) UpdateContent(id string, content []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.files[id]
	f.content = content
	f.modifiedTime = stamp(d.seq())
	d.changes = append(d.changes, changeRecord{fileID: id, file: f})
}

// Rename changes a file's name and records a change.
func (d *Drive) Rename(id, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.files[id]
	f.name = name
	f.modifiedTime = stamp(d.seq())
	d.changes = append(d.changes, changeRecord{fileID: id, file: f})
}

// Trash marks a file trashed and records a removal change.
func (d *Drive) Trash(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.files[id]
	f.trashed = true
	d.changes = append(d.changes, changeRecord{fileID: id, removed: true, file: f})
}

// seq returns the next fake clock tick.
func (d *Drive) seq() int {
	d.clock++
	return d.clock
}

// Client builds a google provider client pointed at the fake server.
func (d *Drive) Client(t testing.TB) *google.Client {
	t.Helper()
	hc := &http.Client{}
	client, err := google.New(context.Background(), hc, option.WithEndpoint(d.srv.URL+"/drive/v3/"))
	if err != nil {
		t.Fatal(err)
	}
	return client
}
