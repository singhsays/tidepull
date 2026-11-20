package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/model"
	"github.com/singhsays/tidepull/internal/state"
)

// applyChange applies one change-stream entry to the local tree.
func (e *Engine) applyChange(ctx context.Context, acct config.Account, res *resolver, ch *model.Change, files map[string]state.File) error {
	if ch.Removed || ch.File == nil { // trashed entries arrive as removed
		if ch.Path != "" {
			return e.removeByPath(acct, res, files, ch.Path)
		}
		return e.removeByID(acct, res, files, ch.FileID)
	}
	return e.placeFile(ctx, acct, res, files, toStateFile(ch.File), files[ch.FileID])
}

// placeFile resolves the file's scope and local path, then renames,
// downloads or skips it accordingly. The old row, when known, is passed
// separately so rename detection sees the pre-change state.
func (e *Engine) placeFile(ctx context.Context, acct config.Account, res *resolver, files map[string]state.File, f state.File, old state.File) error {
	if !res.inScope(ctx, files, f) {
		e.Log.Debug().Str("file", f.Name).Str("id", f.ID).Msg("file out of scope")
		return nil
	}
	if f.Folder {
		if err := e.renameFolder(acct, res, files, f); err != nil {
			return err
		}
		if err := e.DB.FileUpsert(acct.Name, f); err != nil {
			return err
		}
		files[f.ID] = f
		return nil
	}
	if res.excluded(acct, f) {
		e.Log.Debug().Str("file", f.Name).Str("id", f.ID).Msg("file excluded")
		return nil
	}

	known := old.ID == f.ID
	if known && old.Hash != "" && old.Hash == f.Hash && old.Name == f.Name && old.ParentID == f.ParentID {
		return nil
	}

	newPath := e.localPath(acct, resolvedPath(res.scopeRoot, files, f))
	if known {
		oldPath := e.localPath(acct, resolvedPath(res.scopeRoot, files, old))
		if oldPath != newPath {
			if err := os.Rename(oldPath, newPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			e.Log.Info().Str("from", relLocalPath(acct, oldPath)).Str("to", relLocalPath(acct, newPath)).Msg("renamed")
		}
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return err
	}
	e.warnDuplicate(acct, res, files, f, newPath)

	target, err := res.client.ExportMime(f.MimeType, acct.Export)
	if err != nil {
		return err
	}
	if !known || old.Hash != f.Hash {
		written, err := e.download(ctx, res.client, f, newPath, target)
		if err != nil {
			return err
		}
		if f.Hash == "" {
			f.Hash = written
		}
		e.Log.Info().Str("path", relLocalPath(acct, newPath)).Int64("size", f.Size).Msg("stored")
	}
	if err := e.DB.FileUpsert(acct.Name, f); err != nil {
		return err
	}
	files[f.ID] = f
	return nil
}

// download streams the file content — exported as targetMime when set —
// to a temp file, verifies the content hash and renames it into place.
// It returns the hash of the content written; exported content has no
// remote checksum, so the caller records this as the file's hash.
func (e *Engine) download(ctx context.Context, client Client, f state.File, localPath, targetMime string) (string, error) {
	var body io.ReadCloser
	var err error
	if targetMime != "" {
		body, err = client.Export(ctx, f.ID, targetMime)
	} else {
		body, err = client.Download(ctx, f.ID)
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(localPath), ".tidepull-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	hash := client.NewContentHash()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if f.Hash != "" && got != f.Hash {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("hash mismatch for %s: got %s, want %s", f.Name, got, f.Hash)
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		return "", err
	}
	return got, nil
}

// removeByID applies a provider removal keyed by file ID: dispose of
// the local copy per the account's deletion policy, then forget the row.
func (e *Engine) removeByID(acct config.Account, res *resolver, files map[string]state.File, fileID string) error {
	row, ok := files[fileID]
	if !ok {
		return nil
	}
	if err := e.disposeLocal(acct, res, files, row); err != nil {
		return err
	}
	if err := e.DB.FileDelete(acct.Name, fileID); err != nil {
		return err
	}
	delete(files, fileID)
	return nil
}

// removeByPath matches a provider removal recorded by path rather than
// by ID against known rows, including folder rows.
func (e *Engine) removeByPath(acct config.Account, res *resolver, files map[string]state.File, path string) error {
	want := strings.Split(strings.Trim(path, "/"), "/")
	for id, row := range files {
		if !slices.Equal(want, resolvedPath(res.scopeRoot, files, row)) {
			continue
		}
		return e.removeByID(acct, res, files, id)
	}
	return nil
}

// disposeLocal applies the account's deletion policy to one known row.
func (e *Engine) disposeLocal(acct config.Account, res *resolver, files map[string]state.File, row state.File) error {
	if acct.OnDeletion == config.DeletionArchive {
		return e.archiveRow(acct, res, files, row)
	}
	return e.removeRow(acct, res, files, row)
}

// removeRow deletes the local copy of one remote row. Folder rows
// leave their local directories behind; their children remove the files.
func (e *Engine) removeRow(acct config.Account, res *resolver, files map[string]state.File, row state.File) error {
	if row.Folder {
		return nil
	}
	path := e.localPath(acct, resolvedPath(res.scopeRoot, files, row))
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	e.Log.Info().Str("path", relLocalPath(acct, path)).Msg("removed")
	return nil
}

// renameFolder renames or moves the local directory of a changed folder.
func (e *Engine) renameFolder(acct config.Account, res *resolver, files map[string]state.File, f state.File) error {
	old, known := files[f.ID]
	if !known || (old.Name == f.Name && old.ParentID == f.ParentID) {
		return nil
	}
	oldPath := e.localPath(acct, resolvedPath(res.scopeRoot, files, old))
	files[f.ID] = f
	newPath := e.localPath(acct, resolvedPath(res.scopeRoot, files, f))
	files[f.ID] = old
	if oldPath == "" || oldPath == newPath {
		return nil
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(newPath, 0o755); err != nil {
			return err
		}
	}
	e.Log.Info().Str("from", relLocalPath(acct, oldPath)).Str("to", relLocalPath(acct, newPath)).Msg("renamed")
	return nil
}

// warnDuplicate logs the last-wins policy when another known file maps
// to the same local path.
func (e *Engine) warnDuplicate(acct config.Account, res *resolver, files map[string]state.File, f state.File, localPath string) {
	for id, row := range files {
		if id == f.ID || row.Folder || res.excluded(acct, row) {
			continue
		}
		if e.localPath(acct, resolvedPath(res.scopeRoot, files, row)) == localPath {
			e.Log.Warn().Str("path", relLocalPath(acct, localPath)).Str("kept", f.ID).Str("overwritten", id).Msg("duplicate filename on provider, last write wins")
		}
	}
}
