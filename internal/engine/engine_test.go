package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, err)
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
	engine := &Engine{DB: h.db, Log: zerolog.New(&buf).Level(zerolog.DebugLevel)}
	require.NoError(t, engine.Pull(context.Background(), h.acct, h.d.Client(t)))
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
	require.False(t, logs.has(`"level":"warn"`), "unexpected warnings: %s", logs.b.String())
	got, err := os.ReadFile(filepath.Join(h.local, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))
	got, err = os.ReadFile(filepath.Join(h.local, "docs", "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "bravo", string(got))
}

func TestModifyReDownloads(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("v1"))
	h.run(t)

	h.d.UpdateContent("a", []byte("v2-longer"))
	h.run(t)
	got, err := os.ReadFile(filepath.Join(h.local, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "v2-longer", string(got))
}

func TestRenameFile(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "old.txt", drivetest.RootID, "text/plain", []byte("data"))
	h.run(t)

	h.d.Rename("a", "new.txt")
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "old.txt"))
	got, err := os.ReadFile(filepath.Join(h.local, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "data", string(got))
}

func TestRenameFolderMovesSubtree(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("dir", "docs", drivetest.RootID, google.FolderMime, nil)
	h.d.AddFile("b", "b.txt", "dir", "text/plain", []byte("bravo"))
	h.run(t)

	h.d.Rename("dir", "papers")
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "docs"))
	got, err := os.ReadFile(filepath.Join(h.local, "papers", "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "bravo", string(got))
}

func TestTrashRemovesLocal(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("hello"))
	h.run(t)

	h.d.Trash("a")
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "a.txt"))
}

func TestNativeDocSkipped(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("d", "doc", drivetest.RootID, drivetest.DocMime, []byte("irrelevant"))
	logs := h.run(t)

	require.NoFileExists(t, filepath.Join(h.local, "doc"))
	require.True(t, logs.has(`"message":"file excluded"`), "want skip log, got: %s", logs.b.String())
}

func TestDuplicateLastWriteWins(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "same.txt", drivetest.RootID, "text/plain", []byte("first"))
	h.d.AddFile("c", "same.txt", drivetest.RootID, "text/plain", []byte("second"))
	logs := h.run(t)

	got, err := os.ReadFile(filepath.Join(h.local, "same.txt"))
	require.NoError(t, err)
	require.Equal(t, "second", string(got))
	require.True(t, logs.has("last write wins"), "want duplicate warning, got: %s", logs.b.String())
}

func TestExcludeAdditionalMime(t *testing.T) {
	h := newHarness(t)
	h.acct.Exclude = []string{"text/plain"}
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("skip me"))
	h.d.AddFile("p", "p.png", drivetest.RootID, "image/png", []byte("keep me"))
	h.run(t)

	require.NoFileExists(t, filepath.Join(h.local, "a.txt"))
	got, err := os.ReadFile(filepath.Join(h.local, "p.png"))
	require.NoError(t, err)
	require.Equal(t, "keep me", string(got))
}

func TestExcludeChangeRemovesLocal(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("data"))
	h.run(t)

	h.acct.Exclude = []string{"text/plain"}
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "a.txt"))
}

func TestExcludeRemovedDownloadsAgain(t *testing.T) {
	h := newHarness(t)
	h.acct.Exclude = []string{"text/plain"}
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("data"))
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "a.txt"))

	h.acct.Exclude = nil
	h.run(t)
	got, err := os.ReadFile(filepath.Join(h.local, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "data", string(got))
}

func TestFolderScopeLimitsSync(t *testing.T) {
	h := newHarness(t)
	h.acct.FolderID = "keep"
	h.d.AddFile("keep", "keep", drivetest.RootID, google.FolderMime, nil)
	h.d.AddFile("in", "in.txt", "keep", "text/plain", []byte("in"))
	h.d.AddFile("out", "out.txt", drivetest.RootID, "text/plain", []byte("out"))
	h.run(t)

	got, err := os.ReadFile(filepath.Join(h.local, "in.txt"))
	require.NoError(t, err)
	require.Equal(t, "in", string(got))
	require.NoFileExists(t, filepath.Join(h.local, "out.txt"))
}
