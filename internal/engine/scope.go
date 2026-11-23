package engine

import (
	"context"
	"path/filepath"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/model"
	"github.com/singhsays/tidepull/internal/state"
)

// resolver answers ancestry and scope questions for one pull cycle,
// caching ancestors it had to fetch.
type resolver struct {
	client Client
	// scopeRoot is the provider ID of the folder being synced. Empty
	// means the account's whole root, which for some providers has no
	// ID of its own.
	scopeRoot string
	// stats accumulates the cycle's local changes; the resolver is
	// created once per cycle.
	stats cycleStats
}

// cycleStats counts one cycle's local changes.
type cycleStats struct {
	adds, updates, removes int
}

// newResolver resolves the scope root's provider ID once per cycle.
// Empty FolderID means the whole account root, which the provider
// resolves from the "root" sentinel.
func newResolver(ctx context.Context, client Client, folderID string) (*resolver, error) {
	lookup := folderID
	if lookup == "" {
		lookup = "root"
	}
	root, err := client.GetFile(ctx, lookup)
	if err != nil {
		return nil, err
	}
	return &resolver{client: client, scopeRoot: root.ID}, nil
}

// inScope walks the file's ancestor chain (fetching and caching missing
// ancestors) and reports whether it reaches the scope root. An empty
// scope root means the whole account is in scope.
func (r *resolver) inScope(ctx context.Context, files map[string]state.File, f state.File) bool {
	if r.scopeRoot == "" {
		return true
	}
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

// toStateFile converts a provider file to a state row.
func toStateFile(f *model.File) state.File {
	return state.File{
		ID:       f.ID,
		ParentID: f.ParentID,
		Name:     f.Name,
		MimeType: f.MimeType,
		Folder:   f.Folder,
		Hash:     f.Hash,
		Size:     f.Size,
	}
}

// resolvedPath walks the parent chain of start and its cached ancestors,
// returning the path relative to the scope root (no account dir). The
// walk stops at the scope root row or an unknown parent.
func resolvedPath(scopeRoot string, files map[string]state.File, start state.File) []string {
	rel, _ := resolvedRel(scopeRoot, files, start)
	return rel
}

// resolvedRel walks the parent chain of start and its cached ancestors,
// returning the path relative to the scope root and whether the chain
// reaches the scope root (or, with an empty scope root, the account
// root) through known rows.
func resolvedRel(scopeRoot string, files map[string]state.File, start state.File) ([]string, bool) {
	var rel []string
	row := start
	for depth := 0; depth < 64; depth++ {
		if row.ID == scopeRoot {
			return rel, true
		}
		rel = append([]string{row.Name}, rel...)
		// The scope root row itself may be absent from files; reaching it
		// as a parent still completes the chain.
		if row.ParentID == "" {
			return rel, scopeRoot == ""
		}
		if row.ParentID == scopeRoot {
			return rel, true
		}
		var ok bool
		row, ok = files[row.ParentID]
		if !ok {
			return rel, false
		}
	}
	return rel, false
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
