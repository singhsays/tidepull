package main

import (
	"context"

	"github.com/singhsays/tidepull/internal/config"
)

// runOnce performs one pull cycle per account, then returns.
func runOnce(ctx context.Context, cfg *config.Config) error {
	return errNotImplemented
}

// runDaemon polls each account on its interval until ctx is done.
func runDaemon(ctx context.Context, cfg *config.Config) error {
	return errNotImplemented
}

// authLogin runs the OAuth loopback flow and stores the refresh token.
func authLogin(ctx context.Context, acct config.Account) error {
	return errNotImplemented
}
