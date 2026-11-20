// Archive disposal for the account's on-deletion policy: locally kept
// copies of remotely deleted files, one UTC-timestamped event directory
// per deletion.
package engine

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/state"
)

// archiveName is the directory under the account's local dir that holds
// archived copies of files the remote deleted.
const archiveName = ".tidepull-archive"

// archiveTimestamp renders one deletion event's subdirectory name.
func archiveTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15-04-05Z")
}

// archiveRow moves the local copy of one remote row into the account's
// archive under the deletion's UTC timestamp, preserving its relative
// path. Folder rows move their whole subtree, which also covers files
// whose ancestry was already lost to an earlier removal. The scope root
// itself has no path of its own to archive.
func (e *Engine) archiveRow(acct config.Account, res *resolver, files map[string]state.File, row state.File) error {
	rel, intact := resolvedRel(res.scopeRoot, files, row)
	if len(rel) == 0 {
		return nil
	}
	if row.Folder {
		return e.archiveDir(acct, rel)
	}
	if !intact {
		// The parent chain no longer resolves because the folder above
		// was removed first; its subtree took this file with it.
		e.Log.Debug().Str("file", row.Name).Msg("file already archived with its folder")
		return nil
	}
	return e.archivePath(acct, rel)
}

// archivePath moves the file at the row's local path into the archive.
func (e *Engine) archivePath(acct config.Account, rel []string) error {
	src := e.localPath(acct, rel)
	if _, err := os.Lstat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	dst := e.archiveTarget(acct, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	e.Log.Info().Str("from", relLocalPath(acct, src)).Str("to", relLocalPath(acct, dst)).Msg("archived")
	return nil
}

// archiveDir moves the folder's local subtree into the archive. When
// descendants were archived individually first, their directories
// already occupy the target; the subtree's content is then safe there.
func (e *Engine) archiveDir(acct config.Account, rel []string) error {
	src := e.localPath(acct, rel)
	if _, err := os.Lstat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	dst := e.archiveTarget(acct, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		if _, statErr := os.Lstat(dst); statErr == nil {
			e.Log.Debug().Str("path", relLocalPath(acct, dst)).Msg("folder already archived by its descendants")
			return nil
		}
		return err
	}
	e.Log.Info().Str("from", relLocalPath(acct, src)).Str("to", relLocalPath(acct, dst)).Msg("archived")
	return nil
}

// archiveTarget joins the account's archive event directory with the
// row's relative path.
func (e *Engine) archiveTarget(acct config.Account, rel []string) string {
	parts := append([]string{acct.LocalDir, archiveName, archiveTimestamp()}, rel...)
	return filepath.Join(parts...)
}
