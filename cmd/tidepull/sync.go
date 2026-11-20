package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/singhsays/tidepull/internal/auth"
	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/engine"
	"github.com/singhsays/tidepull/internal/provider"
	"github.com/singhsays/tidepull/internal/provider/google"
	"github.com/singhsays/tidepull/internal/state"
)

// openState opens the default state database.
func openState() (*state.DB, error) {
	path, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	return state.Open(path)
}

// runOnce performs one pull cycle per account, then returns.
func runOnce(ctx context.Context, cfg *config.Config) error {
	db, err := openState()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	var errs []error
	for _, acct := range cfg.Accounts {
		if err := pullAccount(ctx, db, acct); err != nil {
			errs = append(errs, fmt.Errorf("account %q: %w", acct.Name, err))
		}
	}
	return errors.Join(errs...)
}

// runDaemon polls each account on its interval until ctx is done.
func runDaemon(ctx context.Context, cfg *config.Config) error {
	db, err := openState()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	var wg sync.WaitGroup
	for _, acct := range cfg.Accounts {
		wg.Add(1)
		go func(acct config.Account) {
			defer wg.Done()
			if err := pullAccount(ctx, db, acct); err != nil {
				log.Error().Str("account", acct.Name).Err(err).Msg("pull failed")
			}
			ticker := time.NewTicker(acct.Interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := pullAccount(ctx, db, acct); err != nil {
						log.Error().Str("account", acct.Name).Err(err).Msg("pull failed")
					}
				}
			}
		}(acct)
	}
	wg.Wait()
	return ctx.Err()
}

// pullAccount pulls one account's tree into its local directory.
func pullAccount(ctx context.Context, db *state.DB, acct config.Account) error {
	spec, err := provider.Get(acct.Provider)
	if err != nil {
		return err
	}
	hc, err := auth.Client(ctx, acct, spec, db)
	if err != nil {
		return err
	}
	client, err := google.New(ctx, hc)
	if err != nil {
		return err
	}
	acctLog := log.Logger.With().Str("account", acct.Name).Logger()
	acctEngine := &engine.Engine{DB: db, Log: acctLog}
	return acctEngine.Pull(ctx, acct, client)
}

// authLogin runs the OAuth loopback flow and stores the refresh token.
func authLogin(ctx context.Context, acct config.Account) error {
	spec, err := provider.Get(acct.Provider)
	if err != nil {
		return err
	}
	path, err := state.DefaultPath()
	if err != nil {
		return err
	}
	db, err := state.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := auth.Login(ctx, acct, spec, db); err != nil {
		return err
	}
	fmt.Printf("Authorized %q. Token stored in %s\n", acct.Name, path)
	return nil
}
