// Package auth performs OAuth2 authorization-code login over a localhost
// redirect and builds auto-refreshing HTTP clients for accounts.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

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

// Secret reads and trims the account's client secret file.
func Secret(acct config.Account) (string, error) {
	raw, err := os.ReadFile(acct.ClientSecretFile)
	if err != nil {
		return "", fmt.Errorf("client secret: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", fmt.Errorf("client secret: %s is empty", acct.ClientSecretFile)
	}
	return secret, nil
}

// oauthConfig assembles the OAuth2 config for an account. The redirect URL
// is filled in per login attempt (loopback with a random free port).
func oauthConfig(acct config.Account, spec provider.Spec, secret, redirectURL string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     acct.ClientID,
		ClientSecret: secret,
		Endpoint:     spec.Endpoint,
		RedirectURL:  redirectURL,
		Scopes:       acct.Scopes,
	}
}

// Login runs the authorization-code flow: bind a localhost listener, open
// the provider's consent page in a browser, exchange the redirect's code
// (with PKCE) and store the returned token.
func Login(ctx context.Context, acct config.Account, spec provider.Spec, store TokenStore) (*oauth2.Token, error) {
	secret, err := Secret(acct)
	if err != nil {
		return nil, err
	}

	verifier := oauth2.GenerateVerifier()
	state, err := randomState()
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = l.Close() }()

	conf := oauthConfig(acct, spec, secret, fmt.Sprintf("http://127.0.0.1:%d/", l.Addr().(*net.TCPAddr).Port))
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
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
	secret, err := Secret(acct)
	if err != nil {
		return nil, err
	}
	tok, err := store.TokenLoad(acct.Name)
	if err != nil {
		return nil, err
	}
	conf := oauthConfig(acct, spec, secret, "")
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
