// Package engine applies provider change streams to local directories.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"sort"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/provider/google"
	"github.com/singhsays/tidepull/internal/state"
)

// Engine pulls one account's tree into its local directory.
type Engine struct {
	DB  *state.DB
	Log *slog.Logger
}

// Pull brings the local directory up to date with the provider: delta
// from the stored page token, or a full resync when there is none.
func (e *Engine) Pull(ctx context.Context, acct config.Account, client Client) error {
	res, err := newResolver(ctx, client, acct.FolderID)
	if err != nil {
		return err
	}
	files, err := e.DB.Files(acct.Name)
	if err != nil {
		return err
	}
	pageToken, err := e.DB.PageTokenLoad(acct.Name)
	if err != nil {
		return err
	}
	if pageToken == "" {
		return e.fullSync(ctx, acct, res, files)
	}
	for {
		changes, next, end, err := client.Changes(ctx, pageToken)
		if err != nil {
			if errors.Is(err, google.ErrStaleChangeToken) {
				e.Log.Warn("stale change token, resyncing", "account", acct.Name)
				return e.fullSync(ctx, acct, res, files)
			}
			return err
		}
		for _, ch := range changes {
			if err := e.applyChange(ctx, acct, res, ch, files); err != nil {
				return err
			}
		}
		if err := e.DB.PageTokenSave(acct.Name, next); err != nil {
			return err
		}
		if end {
			return nil
		}
		pageToken = next
	}
}

// fullSync rebuilds the account's tree from a fresh listing.
func (e *Engine) fullSync(ctx context.Context, acct config.Account, res *resolver, files map[string]state.File) error {
	listed, err := res.client.ListTree(ctx, res.scopeRoot)
	if err != nil {
		return err
	}
	listedByID := map[string]*google.File{}
	for _, f := range listed {
		listedByID[f.ID] = f
	}

	// Local files whose remote entries disappeared are removed.
	for id, row := range files {
		if _, ok := listedByID[id]; !ok && !isFolderRow(row) && !isNativeDocRow(row) {
			if err := e.removeLocal(acct, files, id); err != nil {
				return err
			}
		}
	}
	if err := e.DB.FileDeleteAll(acct.Name); err != nil {
		return err
	}
	files = map[string]state.File{}

	// Apply oldest first so duplicate names resolve deterministically
	// (last wins).
	sort.Slice(listed, func(i, j int) bool {
		return listed[i].ModifiedTime < listed[j].ModifiedTime
	})
	for _, f := range listed {
		if err := e.placeFile(ctx, acct, res, files, toStateFile(f), state.File{}); err != nil {
			return err
		}
	}

	tok, err := res.client.StartPageToken(ctx)
	if err != nil {
		return err
	}
	if err := e.DB.PageTokenSave(acct.Name, tok); err != nil {
		return err
	}
	e.Log.Info("full resync complete", "account", acct.Name, "files", len(listed))
	return nil
}
