package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestLoadDefaults(t *testing.T) {
	path := write(t, `
defaults:
  interval: 45s

accounts:
  - name: photos
    provider: google
    credentials-file: /run/secrets/creds.json
    local-dir: /srv/photos
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	a := cfg.Accounts[0]
	require.Equal(t, 45*time.Second, a.Interval)
	require.NotEmpty(t, a.Scopes, "want provider default scopes")
}

func TestLoadOnDeletion(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: /run/secrets/creds.json
    local-dir: /srv/docs
    on-deletion: archive
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, DeletionArchive, cfg.Accounts[0].OnDeletion)

	path = write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: /run/secrets/creds.json
    local-dir: /srv/docs
`)
	cfg, err = Load(path)
	require.NoError(t, err)
	require.Equal(t, DeletionDelete, cfg.Accounts[0].OnDeletion, "unset must default to delete")

	path = write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: /run/secrets/creds.json
    local-dir: /srv/docs
    on-deletion: maybe
`)
	_, err = Load(path)
	require.Error(t, err)
}

func TestLoadExclude(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: /run/secrets/creds.json
    local-dir: /srv/docs
    exclude:
      - text/csv
      - application/vnd.google-apps.form
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, []string{"text/csv", "application/vnd.google-apps.form"}, cfg.Accounts[0].Exclude)
}

func TestLoadPerAccountInterval(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: /run/secrets/creds.json
    local-dir: /srv/docs
    interval: 2m
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, cfg.Accounts[0].Interval)
}

func TestLoadFallsBackToDefaultInterval(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: /run/secrets/creds.json
    local-dir: /srv/docs
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, defaultInterval, cfg.Accounts[0].Interval)
}

func TestLoadServerDefaults(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.True(t, *cfg.Server.Enabled, "unset enabled must default to true")
	require.Equal(t, "127.0.0.1:8420", cfg.Server.Listen)
}

func TestLoadServerOverrides(t *testing.T) {
	path := write(t, `
server:
  enabled: false
  listen: 127.0.0.1:9000

accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	require.False(t, *cfg.Server.Enabled)
	require.Equal(t, "127.0.0.1:9000", cfg.Server.Listen)

	path = write(t, `
server:
  listen: :8421

accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
`)
	cfg, err = Load(path)
	require.NoError(t, err)
	require.True(t, *cfg.Server.Enabled)
	require.Equal(t, ":8421", cfg.Server.Listen)
}

func TestLoadServerInvalidListen(t *testing.T) {
	for name, listen := range map[string]string{
		"no-port":  "no-port",
		"no-value": "127.0.0.1:",
		"too-many": "127.0.0.1:8420:extra",
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, fmt.Sprintf(`
server:
  listen: %q

accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
`, listen))
			_, err := Load(path)
			require.Error(t, err)
		})
	}
}

func TestLoadDuplicateName(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/more
`)
	_, err := Load(path)
	require.Error(t, err, "want duplicate-name error")
}

func TestLoadMissingFields(t *testing.T) {
	for name, body := range map[string]string{
		"no-name":        "accounts:\n  - provider: google\n    credentials-file: s\n    local-dir: /d\n",
		"bad-name":       "accounts:\n  - name: a/b\n    provider: google\n    credentials-file: s\n    local-dir: /d\n",
		"no-provider":    "accounts:\n  - name: a\n    credentials-file: s\n    local-dir: /d\n",
		"bad-provider":   "accounts:\n  - name: a\n    provider: b2\n    credentials-file: s\n    local-dir: /d\n",
		"no-credentials": "accounts:\n  - name: a\n    provider: google\n    local-dir: /d\n",
		"no-local-dir":   "accounts:\n  - name: a\n    provider: google\n    credentials-file: s\n",
		"too-fast":       "accounts:\n  - name: a\n    provider: google\n    credentials-file: s\n    local-dir: /d\n    interval: 100ms\n",
		"no-accounts":    "defaults:\n  interval: 30s\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, body))
			require.Error(t, err)
		})
	}
}

func TestLoadRedirectURL(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
    redirect-url: https://tidepull.example.com/auth-callback/docs
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		require.Equal(t, "https://tidepull.example.com/auth-callback/docs", cfg.Accounts[0].RedirectURL)
	})

	t.Run("unset means paste mode", func(t *testing.T) {
		path := write(t, `
accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		require.Empty(t, cfg.Accounts[0].RedirectURL)
	})

	for name, raw := range map[string]string{
		"http scheme":               "http://tidepull.example.com/callback",
		"relative":                  "/callback",
		"bare host":                 "tidepull.example.com/callback",
		"reserved root":             "https://tidepull.example.com/",
		"reserved empty":            "https://tidepull.example.com",
		"reserved callback":         "https://tidepull.example.com/callback",
		"reserved callback subpath": "https://tidepull.example.com/callback/",
		"reserved healthz":          "https://tidepull.example.com/healthz",
		"reserved auth":             "https://tidepull.example.com/auth",
		"reserved auth subpath":     "https://tidepull.example.com/auth/photos/start",
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t, fmt.Sprintf(`
accounts:
  - name: docs
    provider: google
    credentials-file: s
    local-dir: /srv/docs
    redirect-url: %q
`, raw))
			_, err := Load(path)
			require.Error(t, err, "want error for redirect-url %q", raw)
		})
	}
}
