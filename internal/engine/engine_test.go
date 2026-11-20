package engine

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/provider/google"
	"github.com/singhsays/tidepull/internal/provider/google/drivetest"
	"github.com/singhsays/tidepull/internal/state"
)

// harness wires an engine, account and fake server together.
type harness struct {
	d     *drivetest.Drive
	db    *state.DB
	acct  config.Account
	local string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := drivetest.New(t)
	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	local := t.TempDir()
	return &harness{
		d:     d,
		db:    db,
		local: local,
		acct: config.Account{
			Name:     "test",
			FolderID: "",
			LocalDir: local,
		},
	}
}

// run cycles the engine once with a logger capturing warnings.
func (h *harness) run(t *testing.T) *bytesLogger {
	t.Helper()
	var buf bytesLogger
	engine := &Engine{DB: h.db, Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	if err := engine.Pull(context.Background(), h.acct, h.d.Client(t)); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// bytesLogger captures log output.
type bytesLogger struct{ b strings.Builder }

func (l *bytesLogger) Write(p []byte) (int, error) { return l.b.Write(p) }

func (l *bytesLogger) has(substr string) bool {
	return strings.Contains(l.b.String(), substr)
}

func TestFullSyncAndDelta(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("hello"))
	h.d.AddFile("docs", "docs", drivetest.RootID, google.FolderMime, nil)
	h.d.AddFile("b", "b.txt", "docs", "text/plain", []byte("bravo"))

	logs := h.run(t)
	if logs.has("level=WARN") {
		t.Errorf("unexpected warnings: %s", logs.b.String())
	}
	if got, err := os.ReadFile(filepath.Join(h.local, "a.txt")); err != nil || string(got) != "hello" {
		t.Errorf("a.txt = %q, err %v, want hello", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(h.local, "docs", "b.txt")); err != nil || string(got) != "bravo" {
		t.Errorf("docs/b.txt = %q, err %v, want bravo", got, err)
	}
}

func TestModifyReDownloads(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("v1"))
	h.run(t)

	h.d.UpdateContent("a", []byte("v2-longer"))
	h.run(t)
	if got, err := os.ReadFile(filepath.Join(h.local, "a.txt")); err != nil || string(got) != "v2-longer" {
		t.Errorf("a.txt = %q, err %v, want v2-longer", got, err)
	}
}

func TestRenameFile(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "old.txt", drivetest.RootID, "text/plain", []byte("data"))
	h.run(t)

	h.d.Rename("a", "new.txt")
	h.run(t)
	if _, err := os.Stat(filepath.Join(h.local, "old.txt")); !os.IsNotExist(err) {
		t.Errorf("old.txt still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(h.local, "new.txt")); err != nil || string(got) != "data" {
		t.Errorf("new.txt = %q, err %v, want data", got, err)
	}
}

func TestRenameFolderMovesSubtree(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("dir", "docs", drivetest.RootID, google.FolderMime, nil)
	h.d.AddFile("b", "b.txt", "dir", "text/plain", []byte("bravo"))
	h.run(t)

	h.d.Rename("dir", "papers")
	h.run(t)
	if _, err := os.Stat(filepath.Join(h.local, "docs")); !os.IsNotExist(err) {
		t.Errorf("docs still exists: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(h.local, "papers", "b.txt")); err != nil || string(got) != "bravo" {
		t.Errorf("papers/b.txt = %q, err %v, want bravo", got, err)
	}
}

func TestTrashRemovesLocal(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("hello"))
	h.run(t)

	h.d.Trash("a")
	h.run(t)
	if _, err := os.Stat(filepath.Join(h.local, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("a.txt still exists: %v", err)
	}
}

func TestNativeDocSkipped(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("d", "doc", drivetest.RootID, drivetest.DocMime, []byte("irrelevant"))
	logs := h.run(t)

	if _, err := os.Stat(filepath.Join(h.local, "doc")); !os.IsNotExist(err) {
		t.Errorf("native doc materialized: %v", err)
	}
	if !logs.has("skipping native doc") {
		t.Errorf("want skip log, got: %s", logs.b.String())
	}
}

func TestDuplicateLastWriteWins(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "same.txt", drivetest.RootID, "text/plain", []byte("first"))
	h.d.AddFile("c", "same.txt", drivetest.RootID, "text/plain", []byte("second"))
	logs := h.run(t)

	if got, err := os.ReadFile(filepath.Join(h.local, "same.txt")); err != nil || string(got) != "second" {
		t.Errorf("same.txt = %q, err %v, want second", got, err)
	}
	if !logs.has("last write wins") {
		t.Errorf("want duplicate warning, got: %s", logs.b.String())
	}
}

func TestFolderScopeLimitsSync(t *testing.T) {
	h := newHarness(t)
	h.acct.FolderID = "keep"
	h.d.AddFile("keep", "keep", drivetest.RootID, google.FolderMime, nil)
	h.d.AddFile("in", "in.txt", "keep", "text/plain", []byte("in"))
	h.d.AddFile("out", "out.txt", drivetest.RootID, "text/plain", []byte("out"))
	h.run(t)

	if got, err := os.ReadFile(filepath.Join(h.local, "in.txt")); err != nil || string(got) != "in" {
		t.Errorf("in.txt = %q, err %v, want in", got, err)
	}
	if _, err := os.Stat(filepath.Join(h.local, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("out.txt should not sync: %v", err)
	}
}
