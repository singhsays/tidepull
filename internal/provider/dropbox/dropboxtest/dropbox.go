package dropboxtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"

	"github.com/singhsays/tidepull/internal/provider/dropbox/contenthash"
)

// fakeServer serves a minimal Dropbox API v2 over HTTP: folder cursors,
// metadata, content download and a path-keyed change stream.
type Dropbox struct {
	mu      sync.Mutex
	entries map[string]*entry // absolute path -> entry
	ids     map[string]string // id -> path
	changes []change
	srv     *httptest.Server
	nextID  int
}

type entry struct {
	id      string
	isDir   bool
	content []byte
}

// change is one entry in the fake change stream, in Dropbox's
// path-keyed shape.
type change struct {
	tag  string // file | folder | deleted
	path string
}

// newFakeServer starts the fake; it closes on test cleanup.
func New(t testing.TB) *Dropbox {
	t.Helper()
	f := &Dropbox{
		entries: map[string]*entry{},
		ids:     map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /2/files/list_folder", f.listFolder)
	mux.HandleFunc("POST /2/files/list_folder/continue", f.listFolderContinue)
	mux.HandleFunc("POST /2/files/list_folder/get_latest_cursor", f.getLatestCursor)
	mux.HandleFunc("POST /2/files/get_metadata", f.getMetadata)
	mux.HandleFunc("POST /2/files/download", f.download)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// APIBase returns the fake server's base URL (RPC and content share it).
func (f *Dropbox) APIBase() string { return f.srv.URL }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, tag string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":         map[string]any{".tag": tag},
		"error_summary": tag,
	})
}

// resolve looks up an entry by path or "id:" reference.
func (f *Dropbox) resolve(ref string) (*entry, string, bool) {
	if strings.HasPrefix(ref, "id:") {
		p, ok := f.ids[ref]
		if !ok {
			return nil, "", false
		}
		return f.entries[p], p, true
	}
	e, ok := f.entries[ref]
	return e, ref, ok
}

func (f *Dropbox) listFolder(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var arg struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&arg)
	root := arg.Path
	if arg.Path != "" {
		_, rp, ok := f.resolve(arg.Path)
		if !ok {
			writeError(w, "path")
			return
		}
		root = rp
	}
	entries := make([]map[string]any, 0, len(f.entries))
	for p, e := range f.entries {
		if root == "" || (p != root && strings.HasPrefix(p, root+"/")) {
			entries = append(entries, f.metadata(e, p))
		}
	}
	writeJSON(w, map[string]any{
		"entries":  entries,
		"cursor":   fmt.Sprint(len(f.changes)),
		"has_more": false,
	})
}

func (f *Dropbox) listFolderContinue(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var arg struct {
		Cursor string `json:"cursor"`
	}
	_ = json.NewDecoder(r.Body).Decode(&arg)
	var since int
	if _, err := fmt.Sscan(arg.Cursor, &since); err != nil || since > len(f.changes) {
		writeError(w, "reset")
		return
	}
	entries := make([]map[string]any, 0, len(f.changes)-since)
	for _, ch := range f.changes[since:] {
		entries = append(entries, f.render(ch))
	}
	writeJSON(w, map[string]any{
		"entries":  entries,
		"cursor":   fmt.Sprint(len(f.changes)),
		"has_more": false,
	})
}

func (f *Dropbox) getLatestCursor(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(w, map[string]any{"cursor": fmt.Sprint(len(f.changes))})
}

func (f *Dropbox) getMetadata(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var arg struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&arg)
	e, p, ok := f.resolve(arg.Path)
	if !ok {
		writeError(w, "path")
		return
	}
	writeJSON(w, f.metadata(e, p))
}

func (f *Dropbox) download(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var arg struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg); err != nil {
		writeError(w, "path")
		return
	}
	e, _, ok := f.resolve(arg.Path)
	if !ok || e.isDir {
		writeError(w, "path")
		return
	}
	_, _ = w.Write(e.content)
}

// metadata renders one entry.
func (f *Dropbox) metadata(e *entry, p string) map[string]any {
	m := map[string]any{
		"id":              e.id,
		"name":            path.Base(p),
		"path_display":    p,
		"server_modified": "2026-01-01T00:00:00Z",
	}
	if e.isDir {
		m[".tag"] = "folder"
		return m
	}
	m[".tag"] = "file"
	m["size"] = len(e.content)
	m["content_hash"] = hashHex(e.content)
	return m
}

// render turns one change record into its API entry shape.
func (f *Dropbox) render(ch change) map[string]any {
	if ch.tag == "deleted" {
		return map[string]any{".tag": "deleted", "path_display": ch.path}
	}
	return f.metadata(f.entries[ch.path], ch.path)
}

func (f *Dropbox) put(p string, e *entry) {
	f.entries[p] = e
	f.ids[e.id] = p
}

func (f *Dropbox) remove(p string) {
	e, ok := f.entries[p]
	if !ok {
		return
	}
	delete(f.entries, p)
	delete(f.ids, e.id)
}

func (f *Dropbox) newID() string {
	f.nextID++
	return fmt.Sprintf("id:%d", f.nextID)
}

// AddFile registers a file and records a change for it.
func (f *Dropbox) AddFile(p string, content []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(p, &entry{id: f.newID(), content: content})
	f.changes = append(f.changes, change{tag: "file", path: p})
}

// AddFolder registers a folder and records a change for it.
func (f *Dropbox) AddFolder(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(p, &entry{id: f.newID(), isDir: true})
	f.changes = append(f.changes, change{tag: "folder", path: p})
}

// UpdateContent changes a file's content and records a change.
func (f *Dropbox) UpdateContent(p string, content []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[p].content = content
	f.changes = append(f.changes, change{tag: "file", path: p})
}

// Rename moves a file or folder subtree, keeping IDs stable, and
// records the new entries before the removals.
func (f *Dropbox) Rename(oldPath, newPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[oldPath]
	if !ok {
		return
	}
	prefix := oldPath + "/"
	var subtree []string
	for p := range f.entries {
		if strings.HasPrefix(p, prefix) {
			subtree = append(subtree, p)
		}
	}
	// New entries first so the client's rename detection sees them
	// before the removals.
	f.put(newPath, e)
	f.changes = append(f.changes, change{tag: tagOf(e), path: newPath})
	for _, p := range subtree {
		child := f.entries[p]
		to := newPath + strings.TrimPrefix(p, oldPath)
		f.put(to, child)
		f.changes = append(f.changes, change{tag: tagOf(child), path: to})
	}
	for _, p := range subtree {
		f.changes = append(f.changes, change{tag: "deleted", path: p})
		f.remove(p)
	}
	f.changes = append(f.changes, change{tag: "deleted", path: oldPath})
	f.remove(oldPath)
}

// Delete removes a file or folder subtree and records removals.
func (f *Dropbox) Delete(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := p + "/"
	var subtree []string
	for q := range f.entries {
		if strings.HasPrefix(q, prefix) {
			subtree = append(subtree, q)
		}
	}
	for _, q := range subtree {
		f.changes = append(f.changes, change{tag: "deleted", path: q})
		f.remove(q)
	}
	f.changes = append(f.changes, change{tag: "deleted", path: p})
	f.remove(p)
}

func hashHex(content []byte) string {
	h, _ := contenthash.HashHex(bytes.NewReader(content))
	return h
}

func tagOf(e *entry) string {
	if e.isDir {
		return "folder"
	}
	return "file"
}
