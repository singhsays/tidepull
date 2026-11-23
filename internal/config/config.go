// Package config loads and validates the tidepull YAML configuration.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/singhsays/tidepull/internal/provider"
)

// Default values applied when the config omits them.
const (
	defaultInterval = 30 * time.Second
	// defaultServerListen is the status server's default bind address.
	defaultServerListen = "127.0.0.1:8420"
)

// Deletion policies for the account's on-deletion setting.
const (
	// DeletionDelete removes the local copy when the remote deletes a
	// file, mirroring the remote tree.
	DeletionDelete = "delete"
	// DeletionArchive moves the local copy into the account's
	// .tidepull-archive directory under the deletion's UTC timestamp,
	// preserving its relative path.
	DeletionArchive = "archive"
)

// Account is one cloud account sync job.
type Account struct {
	// Name identifies the account in logs and CLI flags.
	Name string `yaml:"name"`
	// Provider is the cloud provider key, e.g. "google".
	Provider string `yaml:"provider"`
	// CredentialsFile holds the OAuth client credentials (client ID
	// and secret) as JSON, one pair per account.
	CredentialsFile string `yaml:"credentials-file"`
	// Scopes overrides the provider's default OAuth scopes.
	Scopes []string `yaml:"scopes"`
	// FolderID is the remote folder to pull. Empty means the account's
	// root, including items shared with the account.
	FolderID string `yaml:"folder-id"`
	// LocalDir is where pulled files land.
	LocalDir string `yaml:"local-dir"`
	// Exclude adds MIME types to skip, in addition to the provider's
	// own exclusions for formats it cannot sync.
	Exclude []string `yaml:"exclude"`
	// Export enables exporting provider-native document types, keyed by
	// provider-specific type name to the MIME type to export them as
	// (see the provider's documentation).
	Export map[string]string `yaml:"export"`
	// RedirectPort pins the OAuth loopback port for providers that
	// require pre-registered redirect URIs; 0 binds a random port.
	RedirectPort int `yaml:"redirect-port"`
	// RedirectURL enables proxy mode for daemon authorization: the
	// provider sends the browser to this https URL, which a reverse
	// proxy forwards to the daemon's /callback route. Empty uses the
	// paste fallback at http://127.0.0.1:<redirect-port or 58914>/.
	// Most providers restrict non localhost redirects to https endpoints
	// only, which requires the proxy to support TLS e.g. Traefik with
	// LetsEncrypt certs. The endpoint needs to be reachable by the browser
	// but doesn't need to be public internet routable.
	RedirectURL string `yaml:"redirect-url"`
	// Interval between change polls, e.g. 30s.
	Interval time.Duration `yaml:"interval"`
	// OnDeletion controls the local copy when a file is removed from
	// the remote: "delete" removes it, "archive" moves it into the
	// account's archive directory.
	OnDeletion string `yaml:"on-deletion"`
}

// Server configures the daemon's status-page HTTP server.
type Server struct {
	// Enabled defaults to true; set false to run the daemon without the
	// status server.
	Enabled *bool `yaml:"enabled"`
	// Listen is the address the status server binds. Default
	// 127.0.0.1:8420.
	Listen string `yaml:"listen"`
}

// Config is the tidepull configuration file.
type Config struct {
	Defaults Defaults  `yaml:"defaults"`
	Accounts []Account `yaml:"accounts"`
	Server   Server    `yaml:"server"`
}

// Defaults applies to every account unless overridden.
type Defaults struct {
	Interval time.Duration `yaml:"interval"`
}

// Load reads and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Defaults.Interval == 0 {
		cfg.Defaults.Interval = defaultInterval
	}
	if err := resolveServer(cfg); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if a.Interval == 0 {
			a.Interval = cfg.Defaults.Interval
		}
		if a.OnDeletion == "" {
			a.OnDeletion = DeletionDelete
		}
		if err := a.validate(); err != nil {
			return nil, fmt.Errorf("account %q: %w", a.Name, err)
		}
		spec, err := provider.Get(a.Provider)
		if err != nil {
			return nil, fmt.Errorf("account %q: %w", a.Name, err)
		}
		if len(a.Scopes) == 0 {
			a.Scopes = spec.Scopes
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("account: duplicate name %q", a.Name)
		}
		seen[a.Name] = true
	}
	if len(cfg.Accounts) == 0 {
		return nil, fmt.Errorf("config %s: no accounts", path)
	}
	return cfg, nil
}

// resolveServer fills the status server's defaults and checks that an
// enabled server has a bindable listen address. Hostless forms like
// ":8420" are valid; an empty port is not.
func resolveServer(cfg *Config) error {
	if cfg.Server.Enabled == nil {
		enabled := true
		cfg.Server.Enabled = &enabled
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = defaultServerListen
	}
	if !*cfg.Server.Enabled {
		return nil
	}
	if _, port, err := net.SplitHostPort(cfg.Server.Listen); err != nil || port == "" {
		return fmt.Errorf("server: invalid listen address %q", cfg.Server.Listen)
	}
	return nil
}

// validate checks required fields.
func (a *Account) validate() error {
	if a.Name == "" {
		return fmt.Errorf("missing name")
	}
	if a.Name == "." || a.Name == ".." || strings.ContainsAny(a.Name, `/\`) {
		return fmt.Errorf("invalid name %q", a.Name)
	}
	if a.Provider == "" {
		return fmt.Errorf("missing provider")
	}
	if a.CredentialsFile == "" {
		return fmt.Errorf("missing credentials-file")
	}
	if a.LocalDir == "" {
		return fmt.Errorf("missing local-dir")
	}
	if a.Interval < time.Second {
		return fmt.Errorf("interval %s below 1s minimum", a.Interval)
	}
	if a.OnDeletion != DeletionDelete && a.OnDeletion != DeletionArchive {
		return fmt.Errorf("invalid on-deletion %q (want %q or %q)", a.OnDeletion, DeletionDelete, DeletionArchive)
	}
	if a.RedirectURL != "" {
		if err := validateRedirectURL(a.RedirectURL); err != nil {
			return err
		}
	}
	return nil
}

// validateRedirectURL checks a proxy-mode redirect URL: it must be an
// absolute https URL whose path does not collide with the daemon's own
// routes (/ /, /callback, /healthz, and anything under /auth/).
func validateRedirectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid redirect-url %q: %w", raw, err)
	}
	if !u.IsAbs() {
		return fmt.Errorf("invalid redirect-url %q: must be an absolute URL", raw)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("invalid redirect-url %q: scheme must be https", raw)
	}
	p := path.Clean("/" + u.Path)
	if p == "/" || p == "/callback" || p == "/healthz" || p == "/auth" || strings.HasPrefix(p, "/auth/") {
		return fmt.Errorf("invalid redirect-url %q: path collides with a reserved daemon route", raw)
	}
	return nil
}
