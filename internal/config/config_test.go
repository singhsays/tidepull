package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	path := write(t, `
defaults:
  interval: 45s

accounts:
  - name: photos
    client-id: id
    client-secret-file: /run/secrets/secret
    local-dir: /srv/photos
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Accounts[0]
	if a.Interval != 45*time.Second {
		t.Errorf("interval = %s, want 45s", a.Interval)
	}
}

func TestLoadPerAccountInterval(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    client-id: id
    client-secret-file: /run/secrets/secret
    local-dir: /srv/docs
    interval: 2m
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].Interval; got != 2*time.Minute {
		t.Errorf("interval = %s, want 2m", got)
	}
}

func TestLoadFallsBackToDefaultInterval(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    client-id: id
    client-secret-file: /run/secrets/secret
    local-dir: /srv/docs
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Accounts[0].Interval; got != defaultInterval {
		t.Errorf("interval = %s, want %s", got, defaultInterval)
	}
}

func TestLoadDuplicateName(t *testing.T) {
	path := write(t, `
accounts:
  - name: docs
    client-id: id
    client-secret-file: s
    local-dir: /srv/docs
  - name: docs
    client-id: id
    client-secret-file: s
    local-dir: /srv/more
`)
	if _, err := Load(path); err == nil {
		t.Error("want duplicate-name error, got nil")
	}
}

func TestLoadMissingFields(t *testing.T) {
	for name, body := range map[string]string{
		"no-name":         "accounts:\n  - client-id: id\n    client-secret-file: s\n    local-dir: /d\n",
		"no-client-id":    "accounts:\n  - name: a\n    client-secret-file: s\n    local-dir: /d\n",
		"no-secret":       "accounts:\n  - name: a\n    client-id: id\n    local-dir: /d\n",
		"no-local-dir":    "accounts:\n  - name: a\n    client-id: id\n    client-secret-file: s\n",
		"too-fast":        "accounts:\n  - name: a\n    client-id: id\n    client-secret-file: s\n    local-dir: /d\n    interval: 100ms\n",
		"no-accounts":     "defaults:\n  interval: 30s\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}
