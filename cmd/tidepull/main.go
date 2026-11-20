// Command tidepull keeps local directories in sync with cloud storage.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/alecthomas/kong"

	"github.com/adrg/xdg"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/version"
)

// CLI is the tidepull command line.
type CLI struct {
	Run     RunCmd     `cmd:"" help:"Run one pull cycle per account and exit."`
	Daemon  DaemonCmd  `cmd:"" help:"Poll each account on its interval until interrupted."`
	Auth    AuthCmd    `cmd:"" help:"Manage account OAuth tokens."`
	Version VersionCmd `cmd:"" help:"Print version."`
}

// RunCmd runs one pull cycle.
type RunCmd struct {
	Config   string   `short:"c" help:"Path to config file ($XDG_CONFIG_HOME/tidepull/config.yaml)."`
	Accounts []string `name:"account" help:"Restrict to account(s) by name (repeatable)."`
}

// Run executes the cycle.
func (r *RunCmd) Run() error {
	cfg, err := loadFiltered(r.Config, r.Accounts)
	if err != nil {
		return err
	}
	return runOnce(context.Background(), cfg)
}

// DaemonCmd runs the polling loop.
type DaemonCmd struct {
	Config   string   `short:"c" help:"Path to config file ($XDG_CONFIG_HOME/tidepull/config.yaml)."`
	Accounts []string `name:"account" help:"Restrict to account(s) by name (repeatable)."`
}

// Run starts the daemon.
func (d *DaemonCmd) Run() error {
	cfg, err := loadFiltered(d.Config, d.Accounts)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemon(ctx, cfg)
}

// AuthCmd manages OAuth tokens.
type AuthCmd struct {
	Login LoginCmd `cmd:"" help:"Authorize an account via browser OAuth and store its refresh token."`
}

// LoginCmd authorizes one account.
type LoginCmd struct {
	Config  string `short:"c" help:"Path to config file ($XDG_CONFIG_HOME/tidepull/config.yaml)."`
	Account string `name:"account" arg:"" help:"Account name to authorize."`
}

// Run performs the OAuth loopback flow for the account.
func (l *LoginCmd) Run() error {
	cfg, err := loadFiltered(l.Config, []string{l.Account})
	if err != nil {
		return err
	}
	return authLogin(context.Background(), cfg.Accounts[0])
}

// VersionCmd prints the version.
type VersionCmd struct{}

// Run prints the version.
func (v *VersionCmd) Run() error {
	fmt.Println("tidepull", version.Full())
	return nil
}

// defaultConfigPath is the config used when no -c flag is given.
func defaultConfigPath() string {
	return filepath.Join(xdg.ConfigHome, "tidepull", "config.yaml")
}

// loadFiltered loads the config, restricted to the named accounts.
func loadFiltered(path string, names []string) (*config.Config, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if len(names) == 0 {
		return cfg, nil
	}
	byName := make(map[string]config.Account, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		byName[a.Name] = a
	}
	filtered := &config.Config{Defaults: cfg.Defaults}
	for _, name := range names {
		a, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("account %q not found in %s", name, path)
		}
		filtered.Accounts = append(filtered.Accounts, a)
	}
	return filtered, nil
}

func main() {
	cli := CLI{}
	ctx := kong.Parse(&cli,
		kong.Name("tidepull"),
		kong.Description("Keep local directories in sync with cloud storage."),
		kong.UsageOnError(),
	)
	if err := ctx.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tidepull:", err)
		os.Exit(1)
	}
}
