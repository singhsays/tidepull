package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/singhsays/tidepull/internal/config"
)

func TestSecretTrims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("  s3cr3t\n"), 0o600))
	got, err := Secret(account(path))
	require.NoError(t, err)
	require.Equal(t, "s3cr3t", got)
}

func TestSecretMissing(t *testing.T) {
	_, err := Secret(account(filepath.Join(t.TempDir(), "absent")))
	require.Error(t, err, "want error for missing secret file")
}

func TestSecretEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("\n"), 0o600))
	_, err := Secret(account(path))
	require.Error(t, err, "want error for empty secret file")
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
		require.NoError(t, err)
		_ = resp.Body.Close()
		got := <-ch
		require.Equal(t, "c1", got.code)
		require.Empty(t, got.err)
	})
	t.Run("error", func(t *testing.T) {
		ch := make(chan callbackResult, 1)
		srv := httptest.NewServer(callbackHandler("state1", ch))
		defer srv.Close()
		resp, err := srv.Client().Get(srv.URL + "/?state=state1&error=access_denied")
		require.NoError(t, err)
		_ = resp.Body.Close()
		got := <-ch
		require.Equal(t, "access_denied", got.err)
	})
	t.Run("state mismatch", func(t *testing.T) {
		ch := make(chan callbackResult, 1)
		srv := httptest.NewServer(callbackHandler("state1", ch))
		defer srv.Close()
		resp, err := srv.Client().Get(srv.URL + "/?state=other&code=c1")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		select {
		case got := <-ch:
			require.Fail(t, "unexpected callback result", "%+v", got)
		default:
		}
	})
}

func TestRandomState(t *testing.T) {
	a, err := randomState()
	require.NoError(t, err)
	b, err := randomState()
	require.NoError(t, err)
	require.Len(t, a, 32)
	require.Len(t, b, 32)
	require.NotEqual(t, a, b, "states not random")
	require.NotContains(t, a, " \t\n")
}
