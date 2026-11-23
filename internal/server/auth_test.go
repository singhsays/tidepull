package server

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/provider"
	"github.com/singhsays/tidepull/internal/state"
)

// regexpHref extracts the first href attribute, the authorize link on the
// paste-mode start page.
var regexpHref = regexp.MustCompile(`href="([^"]+)"`)

// writeCredentials writes an OAuth credentials file and returns its path.
func writeCredentials(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	return path
}

// tokenServer serves an OAuth token endpoint returning doc.
func tokenServer(t *testing.T, doc string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testSpecFor returns a spec-lookup func pointing every provider at fake
// endpoints.
func testSpecFor(authURL, tokenURL string) func(string) (provider.Spec, error) {
	return func(string) (provider.Spec, error) {
		return provider.Spec{Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL}}, nil
	}
}

// newAuthTestServer starts the daemon's handler with one account wired to
// fake provider endpoints and a temp state DB. redirectURL configures
// proxy mode when set, paste mode when empty.
func newAuthTestServer(t *testing.T, authURL, tokenURL, redirectURL string) (*httptest.Server, *state.DB) {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	creds := writeCredentials(t, `{"client_id": "id", "client_secret": "s3cr3t"}`)
	cfg := &config.Config{Accounts: []config.Account{{
		Name:            "photos",
		Provider:        "google",
		CredentialsFile: creds,
		RedirectURL:     redirectURL,
	}}}
	s := New(cfg, db)
	s.specFor = testSpecFor(authURL, tokenURL)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, db
}

// startAuth begins the account's flow and returns the provider authorize
// URL's query parameters. Redirect mode is not followed; the authorize URL
// is read from the 302 Location.
func startAuth(t *testing.T, ts *httptest.Server, expectCode int) url.Values {
	t.Helper()
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(ts.URL + "/auth/photos/start")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, expectCode, res.StatusCode)
	if expectCode == http.StatusFound {
		loc, err := url.Parse(res.Header.Get("Location"))
		require.NoError(t, err)
		return loc.Query()
	}
	body := readBody(t, res)
	m := regexpHref.FindStringSubmatch(body)
	require.NotNil(t, m, "authorize link missing from start page")
	u, err := url.Parse(html.UnescapeString(m[1]))
	require.NoError(t, err)
	return u.Query()
}

// paste completes the account's flow with the raw pasted redirect URL,
// following the no-JS redirect to the result page.
func paste(t *testing.T, ts *httptest.Server, account, pasted string) (*http.Response, string) {
	t.Helper()
	res, err := http.PostForm(fmt.Sprintf("%s/auth/%s/paste", ts.URL, account), url.Values{"url": {pasted}})
	require.NoError(t, err)
	if res.StatusCode == http.StatusSeeOther {
		loc, err := url.Parse(res.Header.Get("Location"))
		require.NoError(t, err)
		_ = res.Body.Close()
		res, err = http.Get(ts.URL + loc.Path)
		require.NoError(t, err)
	}
	defer func() { _ = res.Body.Close() }()
	return res, readBody(t, res)
}

func TestAuthStartRedirectMode(t *testing.T) {
	ts, _ := newAuthTestServer(t, "https://prov.example/auth", "https://prov.example/token", "https://proxy.example/tidepull/cb")

	q := startAuth(t, ts, http.StatusFound)
	require.Equal(t, "https://proxy.example/tidepull/cb", q.Get("redirect_uri"))
	require.NotEmpty(t, q.Get("state"))
	require.NotEmpty(t, q.Get("code_challenge"), "PKCE challenge missing")
}

func TestAuthStartPasteMode(t *testing.T) {
	ts, _ := newAuthTestServer(t, "https://prov.example/auth", "https://prov.example/token", "")

	res, err := http.Get(ts.URL + "/auth/photos/start")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	body := readBody(t, res)

	require.Contains(t, body, "https://prov.example/auth", "authorize URL not rendered as a link")
	require.Contains(t, body, `action="/auth/photos/paste"`)
	require.Contains(t, body, `name="url"`)
}

func TestAuthStartUnknownAccount(t *testing.T) {
	ts, _ := newAuthTestServer(t, "https://prov.example/auth", "https://prov.example/token", "")

	res, err := http.Get(ts.URL + "/auth/nope/start")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusNotFound, res.StatusCode)
}

func TestCallbackFlowStoresToken(t *testing.T) {
	tokenSrv := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	ts, db := newAuthTestServer(t, "https://prov.example/auth", tokenSrv.URL, "https://proxy.example/tidepull/cb")

	q := startAuth(t, ts, http.StatusFound)
	res, err := http.Get(ts.URL + "/callback?code=c1&state=" + url.QueryEscape(q.Get("state")))
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, readBody(t, res), `Authorized photos`)

	tok, err := db.TokenLoad("photos")
	require.NoError(t, err)
	require.Equal(t, "at", tok.AccessToken)
}

func TestCallbackUnknownState(t *testing.T) {
	ts, db := newAuthTestServer(t, "https://prov.example/auth", "https://prov.example/token", "https://proxy.example/tidepull/cb")

	res, err := http.Get(ts.URL + "/callback?code=c1&state=bogus")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
	_, err = db.TokenLoad("photos")
	require.ErrorIs(t, err, state.ErrNoToken)
}

func TestCallbackWrongStateDoesNotConsumeSession(t *testing.T) {
	tokenSrv := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	ts, db := newAuthTestServer(t, "https://prov.example/auth", tokenSrv.URL, "https://proxy.example/tidepull/cb")

	q := startAuth(t, ts, http.StatusFound)
	res, err := http.Get(ts.URL + "/callback?code=c1&state=bogus")
	require.NoError(t, err)
	_ = res.Body.Close()
	require.Equal(t, http.StatusBadRequest, res.StatusCode)

	res, err = http.Get(ts.URL + "/callback?code=c1&state=" + url.QueryEscape(q.Get("state")))
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	tok, err := db.TokenLoad("photos")
	require.NoError(t, err)
	require.Equal(t, "at", tok.AccessToken)
}

func TestPasteFlowStoresToken(t *testing.T) {
	tokenSrv := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	ts, db := newAuthTestServer(t, "https://prov.example/auth", tokenSrv.URL, "")

	q := startAuth(t, ts, http.StatusOK)
	pasted := q.Get("redirect_uri") + "?code=c1&state=" + url.QueryEscape(q.Get("state"))

	res, body := paste(t, ts, "photos", pasted)
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, body, `Authorized photos`)
	tok, err := db.TokenLoad("photos")
	require.NoError(t, err)
	require.Equal(t, "at", tok.AccessToken)
}

func TestPasteSwapsResultInlineWithHTMX(t *testing.T) {
	tokenSrv := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	ts, _ := newAuthTestServer(t, "https://prov.example/auth", tokenSrv.URL, "")

	q := startAuth(t, ts, http.StatusOK)
	pasted := q.Get("redirect_uri") + "?code=c1&state=" + url.QueryEscape(q.Get("state"))

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/auth/photos/paste",
		strings.NewReader(url.Values{"url": {pasted}}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, readBody(t, res), `Authorized photos`)
}

func TestPasteWrongStateFails(t *testing.T) {
	tokenSrv := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	ts, db := newAuthTestServer(t, "https://prov.example/auth", tokenSrv.URL, "")

	q := startAuth(t, ts, http.StatusOK)
	pasted := q.Get("redirect_uri") + "?code=c1&state=bogus"

	_, body := paste(t, ts, "photos", pasted)
	require.Contains(t, body, "no pending authorization matched")
	_, err := db.TokenLoad("photos")
	require.ErrorIs(t, err, state.ErrNoToken)
}

func TestPasteForOtherAccountFails(t *testing.T) {
	tokenSrv := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	creds := writeCredentials(t, `{"client_id": "id", "client_secret": "s3cr3t"}`)
	cfg := &config.Config{Accounts: []config.Account{
		{Name: "photos", Provider: "google", CredentialsFile: creds},
		{Name: "docs", Provider: "google", CredentialsFile: creds},
	}}
	s := New(cfg, db)
	s.specFor = testSpecFor("https://prov.example/auth", tokenSrv.URL)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	q := startAuth(t, ts, http.StatusOK)
	pasted := q.Get("redirect_uri") + "?code=c1&state=" + url.QueryEscape(q.Get("state"))

	_, body := paste(t, ts, "docs", pasted)
	require.Contains(t, body, "no pending authorization matched")

	// The mismatch must not consume photos' pending session.
	_, body = paste(t, ts, "photos", pasted)
	require.Contains(t, body, `Authorized photos`)
}

func TestExpiredSessionRejected(t *testing.T) {
	tokenSrv := tokenServer(t, `{"access_token":"at","refresh_token":"rt","token_type":"bearer"}`)
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	creds := writeCredentials(t, `{"client_id": "id", "client_secret": "s3cr3t"}`)
	cfg := &config.Config{Accounts: []config.Account{
		{Name: "photos", Provider: "google", CredentialsFile: creds},
	}}
	s := New(cfg, db)
	s.specFor = testSpecFor("https://prov.example/auth", tokenSrv.URL)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	q := startAuth(t, ts, http.StatusOK)
	st := q.Get("state")

	// Force every pending session expired.
	s.flows.mu.Lock()
	for k, p := range s.flows.sessions {
		p.expires = time.Now().Add(-time.Second)
		s.flows.sessions[k] = p
	}
	s.flows.mu.Unlock()

	res, err := http.Get(ts.URL + "/callback?code=c1&state=" + url.QueryEscape(st))
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
}

func TestStatusPageHasAuthorizeLinks(t *testing.T) {
	srv := newTestServer(t, nil)

	res, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	body := readBody(t, res)
	for _, acct := range []string{"photos", "docs", "media"} {
		require.Contains(t, body, fmt.Sprintf(`/auth/%s/start`, acct))
	}
}
