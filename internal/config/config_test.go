package config

import (
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
		"no-name":          "accounts:\n  - provider: google\n    credentials-file: s\n    local-dir: /d\n",
		"bad-name":         "accounts:\n  - name: a/b\n    provider: google\n    credentials-file: s\n    local-dir: /d\n",
		"no-provider":      "accounts:\n  - name: a\n    credentials-file: s\n    local-dir: /d\n",
		"bad-provider":     "accounts:\n  - name: a\n    provider: b2\n    credentials-file: s\n    local-dir: /d\n",
		"no-credentials":   "accounts:\n  - name: a\n    provider: google\n    local-dir: /d\n",
		"no-local-dir":     "accounts:\n  - name: a\n    provider: google\n    credentials-file: s\n",
		"too-fast":         "accounts:\n  - name: a\n    provider: google\n    credentials-file: s\n    local-dir: /d\n    interval: 100ms\n",
		"no-accounts":      "defaults:\n  interval: 30s\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(write(t, body))
			require.Error(t, err)
		})
	}
}
