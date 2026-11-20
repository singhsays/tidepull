// Package config loads and validates the tidepull YAML configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/singhsays/tidepull/internal/provider"
)

// Default values applied when the config omits them.
const (
	defaultInterval = 30 * time.Second
)

// Account is one cloud account sync job.
type Account struct {
	// Name identifies the account in logs and CLI flags.
	Name string `yaml:"name"`
	// Provider is the cloud provider key, e.g. "google".
	Provider string `yaml:"provider"`
	// ClientID / ClientSecretFile are the OAuth client credentials;
	// ClientSecretFile is the path to a file holding the secret.
	ClientID         string `yaml:"client-id"`
	ClientSecretFile string `yaml:"client-secret-file"`
	// Scopes overrides the provider's default OAuth scopes.
	Scopes []string `yaml:"scopes"`
	// FolderID is the remote folder to pull. Empty means the account's
	// root, including items shared with the account.
	FolderID string `yaml:"folder-id"`
	// LocalDir is where pulled files land.
	LocalDir string `yaml:"local-dir"`
	// Interval between change polls, e.g. 30s.
	Interval time.Duration `yaml:"interval"`
}

// Config is the tidepull configuration file.
type Config struct {
	Defaults Defaults  `yaml:"defaults"`
	Accounts []Account `yaml:"accounts"`
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
	seen := map[string]bool{}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if a.Interval == 0 {
			a.Interval = cfg.Defaults.Interval
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
	if a.ClientID == "" {
		return fmt.Errorf("missing client-id")
	}
	if a.ClientSecretFile == "" {
		return fmt.Errorf("missing client-secret-file")
	}
	if a.LocalDir == "" {
		return fmt.Errorf("missing local-dir")
	}
	if a.Interval < time.Second {
		return fmt.Errorf("interval %s below 1s minimum", a.Interval)
	}
	return nil
}
