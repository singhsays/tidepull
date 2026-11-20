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

func TestCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"client_id": "id", "client_secret": "s3cr3t"}`), 0o600))
	id, secret, err := Credentials(account(path))
	require.NoError(t, err)
	require.Equal(t, "id", id)
	require.Equal(t, "s3cr3t", secret)
}

func TestCredentialsInstalledWrapper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte(
		`{"installed": {"client_id": "id", "client_secret": "s3cr3t", "other": 1}}`), 0o600))
	id, secret, err := Credentials(account(path))
	require.NoError(t, err)
	require.Equal(t, "id", id)
	require.Equal(t, "s3cr3t", secret)
}

func TestCredentialsMissingFile(t *testing.T) {
	_, _, err := Credentials(account(filepath.Join(t.TempDir(), "absent")))
	require.Error(t, err, "want error for missing credentials file")
}

func TestCredentialsIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"client_id": "id"}`), 0o600))
	_, _, err := Credentials(account(path))
	require.Error(t, err, "want error for missing client_secret")
}

// account builds an Account with only the fields Credentials uses.
func account(credentialsFile string) config.Account {
	return config.Account{CredentialsFile: credentialsFile}
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
