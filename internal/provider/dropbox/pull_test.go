package dropbox_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/engine"
	"github.com/singhsays/tidepull/internal/provider/dropbox"
	"github.com/singhsays/tidepull/internal/provider/dropbox/dropboxtest"
	"github.com/singhsays/tidepull/internal/state"
)

func TestEnginePull(t *testing.T) {
	f := dropboxtest.New(t)
	f.AddFolder("/work")
	f.AddFile("/work/a.txt", []byte("alpha"))
	f.AddFile("/b.txt", []byte("beta"))

	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	local := t.TempDir()
	acct := config.Account{Name: "test", LocalDir: local}

	eng := &engine.Engine{DB: db, Log: zerolog.Nop()}
	require.NoError(t, eng.Pull(context.Background(), acct, dropbox.New(&http.Client{}, "", f.APIBase(), f.APIBase())))

	got, err := os.ReadFile(filepath.Join(local, "work", "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha", string(got))
	got, err = os.ReadFile(filepath.Join(local, "b.txt"))
	require.NoError(t, err)
	require.Equal(t, "beta", string(got))

	// Renaming a folder keeps every file's ID: the local subtree moves
	// with the folder and nothing re-downloads.
	f.Rename("/work", "/docs")
	require.NoError(t, eng.Pull(context.Background(), acct, dropbox.New(&http.Client{}, "", f.APIBase(), f.APIBase())))
	require.NoFileExists(t, filepath.Join(local, "work", "a.txt"))
	got, err = os.ReadFile(filepath.Join(local, "docs", "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "alpha", string(got))

	// Deletions arrive by path, not by ID.
	f.Delete("/docs/a.txt")
	require.NoError(t, eng.Pull(context.Background(), acct, dropbox.New(&http.Client{}, "", f.APIBase(), f.APIBase())))
	require.NoFileExists(t, filepath.Join(local, "docs", "a.txt"))
}

func TestEnginePullScoped(t *testing.T) {
	f := dropboxtest.New(t)
	f.AddFolder("/keep")
	f.AddFile("/keep/in.txt", []byte("in"))
	f.AddFile("/out.txt", []byte("out"))

	db, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	acct := config.Account{Name: "test", LocalDir: t.TempDir(), FolderID: "/keep"}

	eng := &engine.Engine{DB: db, Log: zerolog.Nop()}
	require.NoError(t, eng.Pull(context.Background(), acct, dropbox.New(&http.Client{}, "/keep", f.APIBase(), f.APIBase())))

	require.NoFileExists(t, filepath.Join(acct.LocalDir, "out.txt"))
	got, err := os.ReadFile(filepath.Join(acct.LocalDir, "in.txt"))
	require.NoError(t, err)
	require.Equal(t, "in", string(got))
}
