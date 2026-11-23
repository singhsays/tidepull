// Package auth performs OAuth2 authorization-code login over a localhost
// redirect and builds auto-refreshing HTTP clients for accounts.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/oauth2"

	"github.com/rs/zerolog/log"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/provider"
)

// TokenStore persists account tokens.
type TokenStore interface {
	TokenSave(name string, tok *oauth2.Token) error
	TokenLoad(name string) (*oauth2.Token, error)
}

// Credentials reads the account's credentials file: a JSON object with
// client_id and client_secret fields, either at the top level or under
// the "installed" wrapper used by provider downloads (e.g. Google).
func Credentials(acct config.Account) (id, secret string, err error) {
	raw, err := os.ReadFile(acct.CredentialsFile)
	if err != nil {
		return "", "", fmt.Errorf("credentials: %w", err)
	}
	var doc struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Installed    *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", "", fmt.Errorf("credentials: parse %s: %w", acct.CredentialsFile, err)
	}
	if doc.Installed != nil {
		doc.ClientID = doc.Installed.ClientID
		doc.ClientSecret = doc.Installed.ClientSecret
	}
	if doc.ClientID == "" || doc.ClientSecret == "" {
		return "", "", fmt.Errorf("credentials: %s needs client_id and client_secret", acct.CredentialsFile)
	}
	return doc.ClientID, doc.ClientSecret, nil
}

// oauthConfig assembles the OAuth2 config for an account. The redirect URL
// is filled in per login attempt (loopback with a random free port).
func oauthConfig(id, secret string, spec provider.Spec, scopes []string, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		Endpoint:     spec.Endpoint,
		RedirectURL:  redirectURL,
		Scopes:       scopes,
	}
}

// BeginSession starts an authorization-code flow for an account: it loads
// the credentials, generates the PKCE verifier and state, and returns the
// authorize URL to open plus a session that later exchanges the callback's
// code for a token. redirectURL is where the provider sends the browser
// after consent; it must match what the caller can receive.
func BeginSession(acct config.Account, spec provider.Spec, redirectURL string, store TokenStore) (string, *Session, error) {
	id, secret, err := Credentials(acct)
	if err != nil {
		return "", nil, err
	}
	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return "", nil, err
	}
	conf := oauthConfig(id, secret, spec, acct.Scopes, redirectURL)
	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, spec.AuthParams...)
	return conf.AuthCodeURL(state, opts...), &Session{
		conf:     conf,
		verifier: verifier,
		state:    state,
		store:    store,
		acctName: acct.Name,
	}, nil
}

// Session is an authorization-code flow in progress: it holds the PKCE
// verifier and state from BeginSession until Exchange consumes the code.
type Session struct {
	conf     *oauth2.Config
	verifier string
	state    string
	store    TokenStore
	acctName string
}

// State returns the state parameter the flow started with; every callback
// for this session must carry it back unchanged.
func (s *Session) State() string { return s.state }

// Exchange completes the flow: it swaps the callback's code for a token
// using the session's PKCE verifier, refuses tokens without a refresh
// token, and stores the result under the account's name.
func (s *Session) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	tok, err := s.conf.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", s.verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("provider returned no refresh token; remove the app's access and run auth login again")
	}
	if err := s.store.TokenSave(s.acctName, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

// ErrStateMismatch reports a callback whose state parameter does not match
// the one the flow started with (possible CSRF).
var ErrStateMismatch = errors.New("state mismatch")

// CallbackError reports a redirect that names a failure instead of a code:
// either the provider's error parameter or a missing code.
type CallbackError struct {
	Reason string
}

func (e *CallbackError) Error() string { return "authorization failed: " + e.Reason }

// ParseCallback validates an OAuth redirect's query against the state the
// flow started with and returns the authorization code. Every callback
// entry point must call it before using the code.
func ParseCallback(query url.Values, wantState string) (string, error) {
	if query.Get("state") != wantState {
		return "", ErrStateMismatch
	}
	if e := query.Get("error"); e != "" {
		return "", &CallbackError{Reason: e}
	}
	code := query.Get("code")
	if code == "" {
		return "", &CallbackError{Reason: "missing code"}
	}
	return code, nil
}

// Login runs the authorization-code flow over a localhost redirect: bind a
// listener, open the provider's consent page in a browser, parse the
// redirect, exchange its code (with PKCE) and store the returned token.
func Login(ctx context.Context, acct config.Account, spec provider.Spec, store TokenStore) (*oauth2.Token, error) {
	// Providers whose redirect URIs must match a pre-registered address
	// (Dropbox) need a fixed port; random ports otherwise.
	listen := "127.0.0.1:0"
	if acct.RedirectPort != 0 {
		listen = fmt.Sprintf("127.0.0.1:%d", acct.RedirectPort)
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listen, err)
	}
	defer func() { _ = l.Close() }()

	authURL, sess, err := BeginSession(acct, spec, fmt.Sprintf("http://127.0.0.1:%d/", l.Addr().(*net.TCPAddr).Port), store)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Authorize %q in your browser:\n\n  %s\n", acct.Name, authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Println("(could not open a browser; open the URL above manually)")
	}

	ch := make(chan callbackResult, 1)
	srv := &http.Server{Handler: callbackHandler(sess.State(), ch)}
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()

	var res callbackResult
	select {
	case res = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.err != "" {
		return nil, fmt.Errorf("authorization failed: %s", res.err)
	}
	return sess.Exchange(ctx, res.code)
}

// callbackResult carries what the redirect told us.
type callbackResult struct {
	code string
	err  string
}

// callbackHandler turns the browser redirect into a callbackResult via
// ParseCallback.
func callbackHandler(wantState string, ch chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, err := ParseCallback(r.URL.Query(), wantState)
		if errors.Is(err, ErrStateMismatch) {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		var cbErr *CallbackError
		if errors.As(err, &cbErr) {
			_, _ = fmt.Fprintf(w, "authorization failed: %s\nYou can close this tab.", cbErr.Reason)
			ch <- callbackResult{err: cbErr.Reason}
			return
		}
		_, _ = fmt.Fprintln(w, "Authorization complete.\nYou can close this tab.")
		ch <- callbackResult{code: code}
	})
}

// randomState returns a fresh hex state parameter.
func randomState() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// openBrowser opens url in the platform's default browser.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// Client returns an HTTP client for the account that refreshes and
// persists tokens automatically.
func Client(ctx context.Context, acct config.Account, spec provider.Spec, store TokenStore) (*http.Client, error) {
	id, secret, err := Credentials(acct)
	if err != nil {
		return nil, err
	}
	tok, err := store.TokenLoad(acct.Name)
	if err != nil {
		return nil, err
	}
	conf := oauthConfig(id, secret, spec, acct.Scopes, "")
	src := conf.TokenSource(ctx, tok)
	return &http.Client{
		Transport: &oauth2.Transport{
			Source: &savingTokenSource{src: src, store: store, name: acct.Name, last: tok},
			Base:   http.DefaultTransport,
		},
	}, nil
}

// savingTokenSource wraps a token source and persists every token it
// hands out that differs from the last one saved, so rotated refresh
// tokens are never lost.
type savingTokenSource struct {
	src   oauth2.TokenSource
	store TokenStore
	name  string
	last  *oauth2.Token
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.src.Token()
	if err != nil {
		return nil, err
	}
	if tok != s.last {
		if err := s.store.TokenSave(s.name, tok); err != nil {
			log.Warn().Err(err).Msg("saving refreshed token")
		} else {
			s.last = tok
		}
	}
	return tok, nil
}
