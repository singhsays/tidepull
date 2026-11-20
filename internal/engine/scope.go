package engine

import (
	"context"
	"path/filepath"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/provider/google"
	"github.com/singhsays/tidepull/internal/state"
)

// resolver answers ancestry and scope questions for one pull cycle,
// caching ancestors it had to fetch.
type resolver struct {
	client Client
	// scopeRoot is the folder being synced, or the account's My Drive
	// root ID when FolderID is empty.
	scopeRoot string
}

// newResolver resolves the scope root once per cycle. Empty FolderID
// means the whole drive, whose root file ID is resolved from "root".
func newResolver(ctx context.Context, client Client, folderID string) (*resolver, error) {
	scopeRoot := folderID
	if scopeRoot == "" {
		root, err := client.GetFile(ctx, "root")
		if err != nil {
			return nil, err
		}
		scopeRoot = root.ID
	}
	return &resolver{client: client, scopeRoot: scopeRoot}, nil
}

// inScope walks the file's ancestor chain (fetching and caching missing
// ancestors) and reports whether it reaches the scope root.
func (r *resolver) inScope(ctx context.Context, files map[string]state.File, f state.File) bool {
	id := f.ParentID
	for depth := 0; depth < 64; depth++ {
		if id == r.scopeRoot {
			return true
		}
		if id == "" {
			return false
		}
		if _, ok := files[id]; !ok {
			got, err := r.client.GetFile(ctx, id)
			if err != nil {
				return false
			}
			files[id] = toStateFile(got)
		}
		id = files[id].ParentID
	}
	return false
}

// toStateFile converts an API file to a state row.
func toStateFile(f *google.File) state.File {
	parent := ""
	if len(f.Parents) > 0 {
		parent = f.Parents[0]
	}
	return state.File{
		ID:       f.ID,
		ParentID: parent,
		Name:     f.Name,
		MimeType: f.MimeType,
		MD5:      f.MD5,
		Size:     f.Size,
	}
}

func isFolderRow(f state.File) bool {
	return f.MimeType == google.FolderMime
}

// resolvedPath walks the parent chain of start and its cached ancestors,
// returning the path relative to the scope root (no account dir).
func resolvedPath(acct config.Account, files map[string]state.File, start state.File) []string {
	var rel []string
	row := start
	for depth := 0; depth < 64; depth++ {
		if acct.FolderID != "" && row.ID == acct.FolderID {
			break
		}
		rel = append([]string{row.Name}, rel...)
		if row.ParentID == "" {
			break
		}
		var ok bool
		row, ok = files[row.ParentID]
		if !ok {
			break
		}
	}
	return rel
}

// localPath joins the account's local dir with the resolved relative
// path.
func (e *Engine) localPath(acct config.Account, rel []string) string {
	parts := append([]string{acct.LocalDir}, rel...)
	return filepath.Join(parts...)
}

// relLocalPath renders a local path relative to the account dir.
func relLocalPath(acct config.Account, path string) string {
	rel, err := filepath.Rel(acct.LocalDir, path)
	if err != nil {
		return path
	}
	return rel
}
