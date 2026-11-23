package engine

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/model"
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

func TestArchiveOnDeletion(t *testing.T) {
	h := newHarness(t)
	h.acct.OnDeletion = config.DeletionArchive
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("hello"))
	h.run(t)

	h.d.Trash("a")
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "a.txt"))
	matches, err := filepath.Glob(filepath.Join(h.local, ".tidepull-archive", "*", "a.txt"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "want one archived copy")
	got, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))
	files, err := h.db.Files(h.acct.Name)
	require.NoError(t, err)
	require.Empty(t, files, "archived file must leave state")
}

func TestArchiveFolderSubtree(t *testing.T) {
	h := newHarness(t)
	h.acct.OnDeletion = config.DeletionArchive
	h.d.AddFile("dir", "dir", drivetest.RootID, google.FolderMime, nil)
	h.d.AddFile("b", "b.txt", "dir", "text/plain", []byte("bravo"))
	h.run(t)

	h.d.Trash("dir")
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "dir", "b.txt"))
	matches, err := filepath.Glob(filepath.Join(h.local, ".tidepull-archive", "*", "dir", "b.txt"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "want the subtree archived whole")
	got, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	require.Equal(t, "bravo", string(got))
}

func TestArchiveKeepsCopyWhenPathReadded(t *testing.T) {
	h := newHarness(t)
	h.acct.OnDeletion = config.DeletionArchive
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("old"))
	h.run(t)
	h.d.Trash("a")
	h.run(t)

	h.d.AddFile("a2", "a.txt", drivetest.RootID, "text/plain", []byte("new"))
	h.run(t)
	got, err := os.ReadFile(filepath.Join(h.local, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(got))
	matches, err := filepath.Glob(filepath.Join(h.local, ".tidepull-archive", "*", "a.txt"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "archive copy must survive a same-path re-add")
	got, err = os.ReadFile(matches[0])
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
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

func TestExportMaterializesNativeDoc(t *testing.T) {
	h := newHarness(t)
	h.acct.Export = map[string]string{"document": "text/plain"}
	content := []byte("# notes\nbody")
	h.d.AddFile("d", "notes.md", drivetest.RootID, drivetest.DocMime, content)
	log := h.run(t)

	got, err := os.ReadFile(filepath.Join(h.local, "notes.md"))
	require.NoError(t, err)
	require.Equal(t, "# notes\nbody", string(got))
	require.False(t, log.has(`"message":"file excluded"`), "exported doc must not be excluded: %s", log.b.String())

	sum := md5.Sum(content)
	rows, err := h.db.Files("test")
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(sum[:]), rows["d"].Hash, "state must record the written content hash")
}

func TestExportUpdatesOnChange(t *testing.T) {
	h := newHarness(t)
	h.acct.Export = map[string]string{"document": "text/plain"}
	h.d.AddFile("d", "notes.md", drivetest.RootID, drivetest.DocMime, []byte("v1"))
	h.run(t)

	h.d.UpdateContent("d", []byte("v2"))
	h.run(t)
	got, err := os.ReadFile(filepath.Join(h.local, "notes.md"))
	require.NoError(t, err)
	require.Equal(t, "v2", string(got))
}

func TestExportRenameMovesLocal(t *testing.T) {
	h := newHarness(t)
	h.acct.Export = map[string]string{"document": "text/plain"}
	h.d.AddFile("d", "old.md", drivetest.RootID, drivetest.DocMime, []byte("data"))
	h.run(t)

	h.d.Rename("d", "new.md")
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "old.md"))
	got, err := os.ReadFile(filepath.Join(h.local, "new.md"))
	require.NoError(t, err)
	require.Equal(t, "data", string(got))
}

func TestUnExportRemovesLocal(t *testing.T) {
	h := newHarness(t)
	h.acct.Export = map[string]string{"document": "text/plain"}
	h.d.AddFile("d", "notes.md", drivetest.RootID, drivetest.DocMime, []byte("data"))
	h.run(t)

	h.acct.Export = nil
	h.run(t)
	require.NoFileExists(t, filepath.Join(h.local, "notes.md"))
}

func TestExportInvalidConfigFails(t *testing.T) {
	h := newHarness(t)
	h.acct.Export = map[string]string{"form": "text/plain"}
	engine := &Engine{DB: h.db, Log: zerolog.Nop()}
	require.Error(t, engine.Pull(context.Background(), h.acct, h.d.Client(t)))
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

// failingClient fails ListTree to exercise a mid-cycle pull failure.
type failingClient struct {
	Client
	err error
}

func (f failingClient) ListTree(ctx context.Context, root string) ([]*model.File, error) {
	return nil, f.err
}

func TestSuccessfulPullRecordsStatus(t *testing.T) {
	h := newHarness(t)
	h.d.AddFile("a", "a.txt", drivetest.RootID, "text/plain", []byte("hello"))
	h.d.AddFile("docs", "docs", drivetest.RootID, google.FolderMime, nil)
	h.d.AddFile("b", "b.txt", "docs", "text/plain", []byte("bravo"))
	h.run(t)

	cs, err := h.db.CycleStatusLoad(h.acct.Name)
	require.NoError(t, err)
	require.Equal(t, state.OutcomeOK, cs.Outcome)
	require.Empty(t, cs.LastError)
	stamp, err := time.Parse(time.RFC3339, cs.LastCycleAt)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().UTC(), stamp, time.Minute)

	count, err := h.db.FileCount(h.acct.Name)
	require.NoError(t, err)
	require.Equal(t, 2, count, "want only downloaded files, not folders")
	require.Equal(t, count, cs.FileCount)
}

func TestFailedPullRecordsStatus(t *testing.T) {
	h := newHarness(t)
	engine := &Engine{DB: h.db, Log: zerolog.Nop()}
	want := errors.New("list exploded")
	err := engine.Pull(context.Background(), h.acct, failingClient{Client: h.d.Client(t), err: want})
	require.ErrorIs(t, err, want)

	cs, err := h.db.CycleStatusLoad(h.acct.Name)
	require.NoError(t, err)
	require.Equal(t, state.OutcomeError, cs.Outcome)
	require.Equal(t, want.Error(), cs.LastError)
}

func TestCanceledPullRecordsNoStatus(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine := &Engine{DB: h.db, Log: zerolog.Nop()}
	err := engine.Pull(ctx, h.acct, h.d.Client(t))
	require.ErrorIs(t, err, context.Canceled)

	_, err = h.db.CycleStatusLoad(h.acct.Name)
	require.ErrorIs(t, err, state.ErrNoStatus)
}
