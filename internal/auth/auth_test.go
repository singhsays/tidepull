package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/provider"
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

// namedAccount builds an Account with the fields the flow primitives use.
func namedAccount(credentialsFile string) config.Account {
	return config.Account{Name: "acct", CredentialsFile: credentialsFile}
}

// testSpec builds a Spec pointing at fake endpoints.
func testSpec(authURL, tokenURL string) provider.Spec {
	return provider.Spec{Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL}}
}

// memStore is a TokenStore that keeps the last saved token in memory.
type memStore struct {
	tok *oauth2.Token
}

func (m *memStore) TokenSave(name string, tok *oauth2.Token) error {
	m.tok = tok
	return nil
}

func (m *memStore) TokenLoad(name string) (*oauth2.Token, error) {
	return m.tok, nil
}

// writeCredentials writes a credentials file and returns its path.
func writeCredentials(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	return path
}

func TestBeginSession(t *testing.T) {
	creds := writeCredentials(t, `{"client_id": "id", "client_secret": "s3cr3t"}`)
	redirect := "http://127.0.0.1:1234/"
	authURL, sess, err := BeginSession(namedAccount(creds), testSpec("https://prov.example/auth", "https://prov.example/token"), redirect, &memStore{})
	require.NoError(t, err)

	u, err := url.Parse(authURL)
	require.NoError(t, err)
	require.Equal(t, "prov.example", u.Host)
	q := u.Query()
	require.Equal(t, redirect, q.Get("redirect_uri"), "authorize URL must contain the redirect URL")
	require.Equal(t, sess.State(), q.Get("state"))
	require.NotEmpty(t, q.Get("code_challenge"), "PKCE challenge missing")
	require.Equal(t, "S256", q.Get("code_challenge_method"))
}

func TestBeginSessionMissingCredentials(t *testing.T) {
	_, _, err := BeginSession(account(filepath.Join(t.TempDir(), "absent")), testSpec("https://a", "https://t"), "http://127.0.0.1:1234/", &memStore{})
	require.Error(t, err)
}

func TestParseCallback(t *testing.T) {
	t.Run("code", func(t *testing.T) {
		code, err := ParseCallback(url.Values{"state": {"state1"}, "code": {"c1"}}, "state1")
		require.NoError(t, err)
		require.Equal(t, "c1", code)
	})
	t.Run("wrong state", func(t *testing.T) {
		_, err := ParseCallback(url.Values{"state": {"other"}, "code": {"c1"}}, "state1")
		require.ErrorIs(t, err, ErrStateMismatch)
	})
	t.Run("missing state", func(t *testing.T) {
		_, err := ParseCallback(url.Values{"code": {"c1"}}, "state1")
		require.ErrorIs(t, err, ErrStateMismatch)
	})
	t.Run("provider error", func(t *testing.T) {
		_, err := ParseCallback(url.Values{"state": {"state1"}, "error": {"access_denied"}}, "state1")
		var cbErr *CallbackError
		require.ErrorAs(t, err, &cbErr)
		require.Equal(t, "access_denied", cbErr.Reason)
	})
	t.Run("missing code", func(t *testing.T) {
		_, err := ParseCallback(url.Values{"state": {"state1"}}, "state1")
		var cbErr *CallbackError
		require.ErrorAs(t, err, &cbErr)
		require.Equal(t, "missing code", cbErr.Reason)
	})
}

// tokenServer serves an OAuth token endpoint returning doc, recording the
// exchanged form.
func tokenServer(t *testing.T, doc string) (*httptest.Server, *[]url.Values) {
	t.Helper()
	forms := &[]url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		*forms = append(*forms, r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, doc)
	}))
	t.Cleanup(srv.Close)
	return srv, forms
}

func TestSessionExchangeStoresToken(t *testing.T) {
	creds := writeCredentials(t, `{"client_id": "id", "client_secret": "s3cr3t"}`)
	srv, forms := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	store := &memStore{}
	_, sess, err := BeginSession(namedAccount(creds), testSpec("https://prov.example/auth", srv.URL), "http://127.0.0.1:1234/", store)
	require.NoError(t, err)

	tok, err := sess.Exchange(context.Background(), "c1")
	require.NoError(t, err)
	require.Equal(t, "at", tok.AccessToken)
	require.Equal(t, tok, store.tok, "token not stored under the account name")
	require.Len(t, *forms, 1)
	require.Equal(t, "c1", (*forms)[0].Get("code"))
	require.NotEmpty(t, (*forms)[0].Get("code_verifier"), "PKCE verifier missing from exchange")
}

func TestSessionExchangeRejectsEmptyRefreshToken(t *testing.T) {
	creds := writeCredentials(t, `{"client_id": "id", "client_secret": "s3cr3t"}`)
	srv, _ := tokenServer(t, `{"access_token":"at","token_type":"bearer"}`)
	store := &memStore{}
	_, sess, err := BeginSession(namedAccount(creds), testSpec("https://prov.example/auth", srv.URL), "http://127.0.0.1:1234/", store)
	require.NoError(t, err)

	_, err = sess.Exchange(context.Background(), "c1")
	require.ErrorContains(t, err, "provider returned no refresh token")
	require.Nil(t, store.tok, "rejected token must not be stored")
}
