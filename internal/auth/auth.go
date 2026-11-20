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

// Login runs the authorization-code flow: bind a localhost listener, open
// the provider's consent page in a browser, exchange the redirect's code
// (with PKCE) and store the returned token.
func Login(ctx context.Context, acct config.Account, spec provider.Spec, store TokenStore) (*oauth2.Token, error) {
	id, secret, err := Credentials(acct)
	if err != nil {
		return nil, err
	}

	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return nil, err
	}
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

	conf := oauthConfig(id, secret, spec, acct.Scopes, fmt.Sprintf("http://127.0.0.1:%d/", l.Addr().(*net.TCPAddr).Port))
	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, spec.AuthParams...)
	authURL := conf.AuthCodeURL(state, opts...)
	fmt.Printf("Authorize %q in your browser:\n\n  %s\n", acct.Name, authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Println("(could not open a browser; open the URL above manually)")
	}

	ch := make(chan callbackResult, 1)
	srv := &http.Server{Handler: callbackHandler(state, ch)}
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

	tok, err := conf.Exchange(ctx, res.code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, errors.New("provider returned no refresh token; remove the app's access and run auth login again")
	}
	if err := store.TokenSave(acct.Name, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

// callbackResult carries what the redirect told us.
type callbackResult struct {
	code string
	err  string
}

// callbackHandler turns the browser redirect into a callbackResult.
func callbackHandler(wantState string, ch chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != wantState {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			_, _ = fmt.Fprintf(w, "authorization failed: %s\nYou can close this tab.", e)
			ch <- callbackResult{err: e}
			return
		}
		_, _ = fmt.Fprintln(w, "Authorization complete.\nYou can close this tab.")
		ch <- callbackResult{code: q.Get("code")}
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
