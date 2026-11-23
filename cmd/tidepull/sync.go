package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rs/zerolog/log"

	"github.com/singhsays/tidepull/internal/auth"
	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/engine"
	"github.com/singhsays/tidepull/internal/provider"
	"github.com/singhsays/tidepull/internal/provider/dropbox"
	"github.com/singhsays/tidepull/internal/provider/google"
	"github.com/singhsays/tidepull/internal/server"
	"github.com/singhsays/tidepull/internal/state"
)

// statusShutdownTimeout bounds how long the status server has to drain
// connections on daemon shutdown.
const statusShutdownTimeout = 5 * time.Second

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

	g, ctx := errgroup.WithContext(ctx)
	for _, acct := range cfg.Accounts {
		g.Go(func() error {
			pullLoop(ctx, db, acct)
			return nil
		})
	}
	startStatusServer(g, ctx, cfg, db)
	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// pullLoop polls the account on its interval until ctx is done.
func pullLoop(ctx context.Context, db *state.DB, acct config.Account) {
	pullCycle(ctx, db, acct)
	ticker := time.NewTicker(acct.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pullCycle(ctx, db, acct)
		}
	}
}

// startStatusServer starts the status server and adds it to the errgroup.
// It adds nothing when disabled or the address cannot bind.
func startStatusServer(g *errgroup.Group, ctx context.Context, cfg *config.Config, db *state.DB) {
	if !*cfg.Server.Enabled {
		return
	}
	srv := &http.Server{
		Addr: cfg.Server.Listen,
		Handler: server.New(cfg, db).Handler(),
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		log.Error().Err(err).Str("addr", srv.Addr).Msg("status server listen failed")
		return
	}
	log.Info().Str("addr", ln.Addr().String()).Msg("status server listening")
	g.Go(func() error {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("status server failed")
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		stopStatusServer(srv)
		return nil
	})
}

// stopStatusServer drains open status-server connections.
func stopStatusServer(srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn().Err(err).Msg("status server shutdown")
	}
}

// pullCycle runs one pull and logs failures, ignoring shutdown.
func pullCycle(ctx context.Context, db *state.DB, acct config.Account) {
	if err := pullAccount(ctx, db, acct); err != nil && !errors.Is(err, context.Canceled) {
		log.Error().Str("account", acct.Name).Err(err).Msg("pull failed")
	}
}

// providerClient builds the account's provider client on the given
// authenticated HTTP client.
func providerClient(ctx context.Context, acct config.Account, hc *http.Client) (engine.Client, error) {
	switch acct.Provider {
	case "google":
		return google.New(ctx, hc)
	case "dropbox":
		return dropbox.New(hc, acct.FolderID, "", ""), nil
	}
	return nil, fmt.Errorf("unknown provider %q", acct.Provider)
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
	client, err := providerClient(ctx, acct, hc)
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
