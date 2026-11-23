// Package engine applies provider change streams to local directories.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

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

// metaIdentity is the meta key holding the authenticated account's name,
// as model.AccountIdentity JSON.
const metaIdentity = "identity"

// Pull brings the local directory up to date with the provider: delta
// from the stored page token, or a full resync when there is none.
// The cycle's outcome is recorded in the status table.
func (e *Engine) Pull(ctx context.Context, acct config.Account, client Client) error {
	start := time.Now()
	stats, err := e.pull(ctx, acct, client)
	if errors.Is(err, context.Canceled) {
		return err
	}
	outcome := state.OutcomeOK
	lastErr := ""
	if err != nil {
		outcome = state.OutcomeError
		lastErr = err.Error()
	}
	fileCount, fcErr := e.DB.FileCount(acct.Name)
	if fcErr != nil {
		fileCount = 0
		e.Log.Error().Err(fcErr).Msg("file count for status failed")
	}
	st := state.CycleStatus{
		Account:     acct.Name,
		LastCycleAt: start.UTC().Format(time.RFC3339),
		Outcome:     outcome,
		LastError:   lastErr,
		FileCount:   fileCount,
		LastOKAt:    start.UTC().Format(time.RFC3339),
		DurationMS:  time.Since(start).Milliseconds(),
	}
	if stats != nil {
		st.Adds, st.Updates, st.Removes = stats.adds, stats.updates, stats.removes
	}
	if err := e.DB.CycleStatusSave(st); err != nil {
		e.Log.Error().Err(err).Msg("cycle status save failed")
	}
	return err
}

// pull runs one full pull cycle, returning the cycle's change counts
// when the cycle got far enough to build its resolver.
func (e *Engine) pull(ctx context.Context, acct config.Account, client Client) (*cycleStats, error) {
	if err := client.CheckExport(acct.Export); err != nil {
		return nil, err
	}
	e.fetchIdentity(ctx, acct, client)
	res, err := newResolver(ctx, client, acct.FolderID)
	if err != nil {
		return nil, err
	}
	files, err := e.DB.Files(acct.Name)
	if err != nil {
		return nil, err
	}
	pageToken, err := e.DB.PageTokenLoad(acct.Name)
	if err != nil {
		return nil, err
	}
	hash := policyHash(acct)
	saved, err := e.DB.MetaLoad(acct.Name, metaPolicyHash)
	if err != nil {
		return nil, err
	}
	if pageToken == "" || saved != hash {
		if pageToken != "" {
			e.Log.Info().Msg("sync policy changed, resyncing")
		}
		if err := e.fullSync(ctx, acct, res, files); err != nil {
			return nil, err
		}
		// Record the policy only after a full sync performed under it.
		return &res.stats, e.DB.MetaSave(acct.Name, metaPolicyHash, hash)
	}
	return &res.stats, e.pullChanges(ctx, acct, res, client, files, pageToken)
}

// fetchIdentity stores the authenticated account's name once, for the
// status page. A failure here never fails the cycle.
func (e *Engine) fetchIdentity(ctx context.Context, acct config.Account, client Client) {
	saved, err := e.DB.MetaLoad(acct.Name, metaIdentity)
	if err == nil && saved != "" {
		return
	}
	id, err := client.AccountInfo(ctx)
	if err != nil {
		e.Log.Warn().Err(err).Msg("account info fetch failed")
		return
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return
	}
	if err := e.DB.MetaSave(acct.Name, metaIdentity, string(encoded)); err != nil {
		e.Log.Error().Err(err).Msg("identity save failed")
	}
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
	// list now covers, are disposed of per the account's deletion policy.
	for id, row := range files {
		if _, listed := listedByID[id]; !listed || res.excluded(acct, row) {
			if err := e.removeByID(acct, res, files, id); err != nil {
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
	fileCount := 0
	for _, f := range listed {
		if !f.Folder {
			fileCount++
		}
	}
	e.Log.Info().Int("files", fileCount).Msg("full resync complete")
	return nil
}
