package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/singhsays/tidepull/internal/auth"
	"github.com/singhsays/tidepull/internal/config"
)

// defaultRedirectPort is the loopback port of the paste-mode redirect URL,
// used when the account sets no redirect-port. Provider apps must have
// http://127.0.0.1:58914/ registered as a redirect URI.
const defaultRedirectPort = 58914

// pendingTTL bounds how long an authorization may stay unfinished.
const pendingTTL = 10 * time.Minute

// pendingAuth is one in-flight authorization: the session begun by
// /auth/{account}/start, plus its expiry.
type pendingAuth struct {
	sess    *auth.Session
	account string
	expires time.Time
}

// flowResult is the outcome shown by the result page.
type flowResult struct {
	ok   bool
	text string
}

// authFlows tracks pending authorizations and their outcomes. Sessions are
// keyed by the OAuth state the provider echoes back; results are keyed by
// account for the page that follows the paste form's redirect.
type authFlows struct {
	mu       sync.Mutex
	sessions map[string]pendingAuth
	results  map[string]flowResult
}

func newAuthFlows() *authFlows {
	return &authFlows{
		sessions: map[string]pendingAuth{},
		results:  map[string]flowResult{},
	}
}

// put stores a pending session, first dropping expired ones.
func (f *authFlows) put(state string, p pendingAuth) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(time.Now())
	f.sessions[state] = p
}

// take removes and returns the pending session named by state, reporting
// false when it is unknown or expired.
func (f *authFlows) take(state string) (pendingAuth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.takeLocked(state, "")
}

// takeFor removes and returns the pending session named by state when it
// belongs to account, reporting false otherwise. A session for a different
// account stays pending.
func (f *authFlows) takeFor(account, state string) (pendingAuth, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.takeLocked(state, account)
}

// takeLocked removes and returns the pending session named by state.
// When owner is non-empty, a session belonging to another account is left
// pending and reported as not found. Callers must hold mu.
func (f *authFlows) takeLocked(state, owner string) (pendingAuth, bool) {
	p, ok := f.sessions[state]
	if !ok {
		return pendingAuth{}, false
	}
	if owner != "" && p.account != owner {
		return pendingAuth{}, false
	}
	delete(f.sessions, state)
	if time.Now().After(p.expires) {
		return pendingAuth{}, false
	}
	return p, true
}

// setResult records an account's last flow outcome.
func (f *authFlows) setResult(account string, r flowResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results[account] = r
}

// takeResult removes and returns the account's last flow outcome.
func (f *authFlows) takeResult(account string) (flowResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.results[account]
	delete(f.results, account)
	return r, ok
}

// sweep drops expired sessions; callers must hold mu.
func (f *authFlows) sweep(now time.Time) {
	for state, p := range f.sessions {
		if now.After(p.expires) {
			delete(f.sessions, state)
		}
	}
}

// authStartData renders the paste-mode start page.
type authStartData struct {
	Account string
	AuthURL string
}

// authResultData renders the result page and the htmx-swapped result.
type authResultData struct {
	Account string
	OK      bool
	Detail  string
}

// handleAuthStart begins an authorization for the named account and either
// redirects to the provider's consent page (redirect-url mode) or renders
// the paste-mode page.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.account(r.PathValue("account"))
	if !ok {
		http.Error(w, "unknown account", http.StatusNotFound)
		return
	}
	redirectURL := acct.RedirectURL
	if redirectURL == "" {
		port := acct.RedirectPort
		if port == 0 {
			port = defaultRedirectPort
		}
		redirectURL = fmt.Sprintf("http://127.0.0.1:%d/", port)
	}
	spec, err := s.specFor(acct.Provider)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	authURL, sess, err := auth.BeginSession(acct, spec, redirectURL, s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.flows.put(sess.State(), pendingAuth{
		sess:    sess,
		account: acct.Name,
		expires: time.Now().Add(pendingTTL),
	})
	if acct.RedirectURL != "" {
		http.Redirect(w, r, authURL, http.StatusFound)
		return
	}
	renderAuth(w, "authstart", authStartData{Account: acct.Name, AuthURL: authURL})
}

// handleCallback completes a flow the provider redirected to /callback.
// The state parameter selects the pending session and is the CSRF check.
func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	p, ok := s.flows.take(r.URL.Query().Get("state"))
	if !ok {
		http.Error(w, "unknown or expired authorization session", http.StatusBadRequest)
		return
	}
	res := s.complete(r.Context(), p, r.URL.Query())
	renderAuth(w, "authresult", authResultData{Account: p.account, OK: res.ok, Detail: res.text})
}

// handleAuthPaste completes a flow from a redirect URL the user pasted.
// Only the pasted URL's query is read. Browsers without JS get a redirect
// to the result page; htmx swaps the result inline.
func (s *Server) handleAuthPaste(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.account(r.PathValue("account"))
	if !ok {
		http.Error(w, "unknown account", http.StatusNotFound)
		return
	}
	res := flowResult{ok: false, text: "could not parse the pasted URL"}
	if u, err := url.Parse(r.FormValue("url")); err == nil {
		res = flowResult{ok: false, text: "no pending authorization matched the pasted URL's state; start again"}
		if p, ok := s.flows.takeFor(acct.Name, u.Query().Get("state")); ok {
			res = s.complete(r.Context(), p, u.Query())
		}
	}
	s.flows.setResult(acct.Name, res)
	if r.Header.Get("HX-Request") == "true" {
		if res.ok {
			w.Header().Set("HX-Redirect", "/")
		}
		renderAuth(w, "authresultbody", authResultData{OK: res.ok, Detail: res.text})
		return
	}
	http.Redirect(w, r, "/auth/"+acct.Name+"/result", http.StatusSeeOther)
}

// handleAuthDisconnect removes the account's stored token and change-stream
// position. The next cycle reports the account as unauthorized, and a later
// authorization starts from a full resync.
func (s *Server) handleAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.account(r.PathValue("account"))
	if !ok {
		http.Error(w, "unknown account", http.StatusNotFound)
		return
	}
	if err := s.db.TokenDelete(acct.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.db.PageTokenDelete(acct.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleAuthResult shows the last flow outcome for the account, or sends
// the browser back to the status page when there is none.
func (s *Server) handleAuthResult(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.account(r.PathValue("account"))
	if !ok {
		http.Error(w, "unknown account", http.StatusNotFound)
		return
	}
	res, ok := s.flows.takeResult(acct.Name)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	renderAuth(w, "authresult", authResultData{Account: acct.Name, OK: res.ok, Detail: res.text})
}

// complete runs the shared completion path for both callback routes: the
// mandatory state check via ParseCallback, then the code exchange.
func (s *Server) complete(ctx context.Context, p pendingAuth, query url.Values) flowResult {
	code, err := auth.ParseCallback(query, p.sess.State())
	if err != nil {
		var cbErr *auth.CallbackError
		if errors.As(err, &cbErr) {
			return flowResult{ok: false, text: cbErr.Error()}
		}
		return flowResult{ok: false, text: "state mismatch: the redirect does not belong to this authorization"}
	}
	if _, err := p.sess.Exchange(ctx, code); err != nil {
		return flowResult{ok: false, text: err.Error()}
	}
	return flowResult{ok: true, text: "Authorized " + p.account}
}

// account returns the configured account with the given name.
func (s *Server) account(name string) (config.Account, bool) {
	for _, a := range s.cfg.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return config.Account{}, false
}

// renderAuth renders a named auth template (a full page or the htmx
// result fragment).
func renderAuth(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		log.Logger.Error().Err(err).Str("template", name).Msg("render auth page")
	}
}
