package google

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
)

// errorServer serves one 403 body and counts requests until stop.
func errorServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func client403(t *testing.T, url string) *Client {
	t.Helper()
	client, err := New(context.Background(), &http.Client{}, option.WithEndpoint(url+"/drive/v3/"))
	require.NoError(t, err)
	return client
}

func TestRetrySkipsPermanent403(t *testing.T) {
	srv, hits := errorServer(t, `{"error":{"code":403,"message":"no","errors":[{"reason":"fileNotDownloadable"}]}}`)
	_, err := client403(t, srv.URL).StartPageToken(context.Background())
	require.Error(t, err)
	require.Equal(t, int32(1), hits.Load(), "permanent 403 must not be retried")
}

func TestRetryRetriesRateLimit403(t *testing.T) {
	srv, hits := errorServer(t, `{"error":{"code":403,"message":"slow down","errors":[{"reason":"rateLimitExceeded"}]}}`)
	_, err := client403(t, srv.URL).StartPageToken(context.Background())
	require.Error(t, err)
	require.Equal(t, int32(4), hits.Load(), "rate-limit 403 must be retried (initial + 3 retries)")
}

func TestCheckExport(t *testing.T) {
	c := &Client{}
	require.NoError(t, c.CheckExport(map[string]string{"document": "text/plain", "drawing": "image/svg+xml"}))
	require.ErrorContains(t, c.CheckExport(map[string]string{"form": "text/plain"}), "unknown type")
	require.ErrorContains(t, c.CheckExport(map[string]string{"document": "image/png"}), "cannot be exported")
}

func TestExportMime(t *testing.T) {
	c := &Client{}
	cfg := map[string]string{"document": "text/plain"}
	target, err := c.ExportMime("application/vnd.google-apps.document", cfg)
	require.NoError(t, err)
	require.Equal(t, "text/plain", target)
	target, err = c.ExportMime("application/vnd.google-apps.spreadsheet", cfg)
	require.NoError(t, err)
	require.Empty(t, target)
}

func TestRetrySkipsUnparseable403(t *testing.T) {
	// Rate-limit 403s always carry a JSON reason; anything else is
	// permanent and fails fast.
	srv, hits := errorServer(t, "forbidden")
	_, err := client403(t, srv.URL).StartPageToken(context.Background())
	require.Error(t, err)
	require.Equal(t, int32(1), hits.Load())
}
