package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/state"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func loadTestConfig(t *testing.T, server string) *config.Config {
	t.Helper()
	path := writeConfig(t, server+`
accounts:
  - name: photos
    provider: google
    credentials-file: s
    local-dir: /srv/photos
`)
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return cfg
}

// freePort reserves a port so startStatusServer can bind it immediately
// after the reservation is released.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func openTestDB(t *testing.T) *state.DB {
	t.Helper()
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestStartStatusServerServesHealthz(t *testing.T) {
	port := freePort(t)
	cfg := loadTestConfig(t, "server:\n  listen: 127.0.0.1:"+strconv.Itoa(port)+"\n")
	db := openTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	startStatusServer(g, gctx, cfg, db)
	t.Cleanup(func() {
		cancel()
		_ = g.Wait()
	})

	addr := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	var res *http.Response
	var err error
	require.Eventually(t, func() bool {
		res, err = http.Get(addr)
		return err == nil
	}, 5*time.Second, 50*time.Millisecond)
	defer func() { _ = res.Body.Close() }()
	require.Equal(t, http.StatusOK, res.StatusCode)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
}

func TestStartStatusServerDisabled(t *testing.T) {
	port := freePort(t)
	cfg := loadTestConfig(t, "server:\n  enabled: false\n  listen: 127.0.0.1:"+strconv.Itoa(port)+"\n")
	db := openTestDB(t)

	g, ctx := errgroup.WithContext(context.Background())
	startStatusServer(g, ctx, cfg, db)
	require.NoError(t, g.Wait())

	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	require.Error(t, err, "server must not bind when disabled")
	if conn != nil {
		_ = conn.Close()
	}
}

func TestStatusServerStopsOnContextCancel(t *testing.T) {
	port := freePort(t)
	cfg := loadTestConfig(t, "server:\n  listen: 127.0.0.1:"+strconv.Itoa(port)+"\n")
	db := openTestDB(t)

	ctx, cancel := context.WithCancel(context.Background())
	g, gctx := errgroup.WithContext(ctx)
	startStatusServer(g, gctx, cfg, db)
	addr := "http://127.0.0.1:" + strconv.Itoa(port) + "/healthz"
	require.Eventually(t, func() bool {
		res, err := http.Get(addr)
		if err != nil {
			return false
		}
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode == http.StatusOK
	}, 5*time.Second, 50*time.Millisecond)

	cancel()
	require.NoError(t, g.Wait(), "all actors must exit after cancel")
	require.Eventually(t, func() bool {
		_, err := http.Get(addr)
		return err != nil
	}, 5*time.Second, 50*time.Millisecond)
}
