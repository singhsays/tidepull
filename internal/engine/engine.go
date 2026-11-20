// Package engine applies provider change streams to local directories.
package engine

import (
	"context"
	"errors"
	"sort"

	"github.com/rs/zerolog"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/model"
	"github.com/singhsays/tidepull/internal/state"
)

// Engine pulls one account's tree into its local directory.
type Engine struct {
	DB  *state.DB
	Log zerolog.Logger
}

// Pull brings the local directory up to date with the provider: delta
// from the stored page token, or a full resync when there is none.
func (e *Engine) Pull(ctx context.Context, acct config.Account, client Client) error {
	if err := client.CheckExport(acct.Export); err != nil {
		return err
	}
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
	hash := policyHash(acct)
	saved, err := e.DB.MetaLoad(acct.Name, metaPolicyHash)
	if err != nil {
		return err
	}
	if pageToken == "" || saved != hash {
		if pageToken != "" {
			e.Log.Info().Msg("sync policy changed, resyncing")
		}
		if err := e.fullSync(ctx, acct, res, files); err != nil {
			return err
		}
		// Record the policy only after a full sync performed under it.
		return e.DB.MetaSave(acct.Name, metaPolicyHash, hash)
	}
	return e.pullChanges(ctx, acct, res, client, files, pageToken)
}

// pullChanges applies change-stream deltas starting from pageToken.
func (e *Engine) pullChanges(ctx context.Context, acct config.Account, res *resolver, client Client, files map[string]state.File, pageToken string) error {
	for {
		changes, next, end, err := client.Changes(ctx, pageToken)
		if err != nil {
			if errors.Is(err, model.ErrStaleChangeToken) {
				e.Log.Warn().Msg("stale change token, resyncing")
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
	listedByID := map[string]*model.File{}
	for _, f := range listed {
		listedByID[f.ID] = f
	}

	// Local files whose remote entries disappeared, or that the exclude
	// list now covers, are removed.
	for id, row := range files {
		if _, listed := listedByID[id]; !listed || res.excluded(acct, row) {
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
	e.Log.Info().Int("files", len(listed)).Msg("full resync complete")
	return nil
}
