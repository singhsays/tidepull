package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/state"
)

func newTestServer(t *testing.T, statuses map[string]state.CycleStatus) *httptest.Server {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, st := range statuses {
		require.NoError(t, db.CycleStatusSave(st))
	}
	cfg := &config.Config{Accounts: []config.Account{
		{Name: "photos", Provider: "google"},
		{Name: "docs", Provider: "dropbox"},
		{Name: "media", Provider: "google"},
	}}
	srv := httptest.NewServer(New(cfg, db).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func TestStatusPageFullDocument(t *testing.T) {
	st := map[string]state.CycleStatus{
		"photos": {Account: "photos", Outcome: state.OutcomeOK, FileCount: 1234},
		"docs":   {Account: "docs", Outcome: state.OutcomeError, LastError: "token expired"},
	}
	srv := newTestServer(t, st)

	res, err := http.Get(srv.URL + "/")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	body := readBody(t, res)

	require.Contains(t, body, "<!DOCTYPE html>")
	for _, want := range []string{
		"photos", "google", "1234",
		"docs", "dropbox", "token expired",
		"media", "not synced yet",
	} {
		require.Contains(t, body, want)
	}
	require.Contains(t, body, `<span class="ok">ok</span>`)
	require.Contains(t, body, `<span class="error">error</span>`)
	require.Regexp(t, `ok</span> · \d+ ms`, body)
	require.Contains(t, body, "every 15s")
	require.Contains(t, body, "tidepull ", "footer should carry the version")
	require.Contains(t, body, "up ", "footer should carry the uptime")
}

func TestRelAge(t *testing.T) {
	// Round-to-minute boundary is unstable at 90s; use 3 minutes.
	recent := time.Now().UTC().Add(-3 * time.Minute).Format(time.RFC3339)
	require.Equal(t, "3m", relAge(recent))
	old := time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)
	require.Equal(t, "3h", relAge(old))
	require.Equal(t, "garbage", relAge("garbage"))
}

func TestStatusPageHTMXRequestReturnsFragmentOnly(t *testing.T) {
	srv := newTestServer(t, nil)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	require.NoError(t, err)
	req.Header.Set("HX-Request", "true")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	body := readBody(t, res)

	require.NotContains(t, body, "<html")
	require.NotContains(t, body, "<!DOCTYPE")
	require.NotContains(t, body, "htmx.min.js")
	require.Contains(t, body, `id="status"`)
	require.Contains(t, body, "not synced yet")
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(t, nil)

	res, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "ok", readBody(t, res))
}

func TestHTMXAssetServed(t *testing.T) {
	srv := newTestServer(t, nil)

	res, err := http.Get(srv.URL + "/assets/htmx.min.js")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Contains(t, readBody(t, res), "htmx")
}

func TestMiddlewareRecoversPanic(t *testing.T) {
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{Accounts: []config.Account{{Name: "photos", Provider: "google"}}}
	srv := New(cfg, db)

	mux := srv.routes()
	mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	handler := withMiddleware(mux)

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	res := httptest.NewRecorder()
	require.NotPanics(t, func() { handler.ServeHTTP(res, req) })
	require.Equal(t, http.StatusInternalServerError, res.Code)
}

func TestMiddlewareRequestLog(t *testing.T) {
	srv := newTestServer(t, nil)

	res, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	return string(b)
}
