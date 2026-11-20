package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/singhsays/tidepull/internal/config"
)

func TestSecretTrims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("  s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Secret(account(path))
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cr3t" {
		t.Errorf("secret = %q, want s3cr3t", got)
	}
}

func TestSecretMissing(t *testing.T) {
	if _, err := Secret(account(filepath.Join(t.TempDir(), "absent"))); err == nil {
		t.Error("want error for missing secret file, got nil")
	}
}

func TestSecretEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Secret(account(path)); err == nil {
		t.Error("want error for empty secret file, got nil")
	}
}

// account builds an Account with only the fields Secret uses.
func account(secretFile string) config.Account {
	return config.Account{ClientSecretFile: secretFile}
}

func TestCallbackHandler(t *testing.T) {
	t.Run("code", func(t *testing.T) {
		ch := make(chan callbackResult, 1)
		srv := httptest.NewServer(callbackHandler("state1", ch))
		defer srv.Close()
		resp, err := srv.Client().Get(srv.URL + "/?state=state1&code=c1")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		got := <-ch
		if got.code != "c1" || got.err != "" {
			t.Errorf("result = %+v, want code c1", got)
		}
	})
	t.Run("error", func(t *testing.T) {
		ch := make(chan callbackResult, 1)
		srv := httptest.NewServer(callbackHandler("state1", ch))
		defer srv.Close()
		resp, err := srv.Client().Get(srv.URL + "/?state=state1&error=access_denied")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		got := <-ch
		if got.err != "access_denied" {
			t.Errorf("err = %q, want access_denied", got.err)
		}
	})
	t.Run("state mismatch", func(t *testing.T) {
		ch := make(chan callbackResult, 1)
		srv := httptest.NewServer(callbackHandler("state1", ch))
		defer srv.Close()
		resp, err := srv.Client().Get(srv.URL + "/?state=other&code=c1")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		select {
		case got := <-ch:
			t.Errorf("unexpected callback result %+v", got)
		default:
		}
	})
}

func TestRandomState(t *testing.T) {
	a, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomState()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 || len(b) != 32 {
		t.Errorf("state length = %d/%d, want 32", len(a), len(b))
	}
	if a == b {
		t.Error("states not random")
	}
	if strings.ContainsAny(a, " \t\n") {
		t.Errorf("state has whitespace: %q", a)
	}
}
