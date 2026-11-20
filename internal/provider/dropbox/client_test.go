package dropbox_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/singhsays/tidepull/internal/model"
	"github.com/singhsays/tidepull/internal/provider/dropbox"
	"github.com/singhsays/tidepull/internal/provider/dropbox/dropboxtest"
)

func newClient(t *testing.T, f *dropboxtest.Dropbox, folderID string) *dropbox.Client {
	t.Helper()
	return dropbox.New(&http.Client{}, folderID, f.APIBase(), f.APIBase())
}

func TestFullListAndDelta(t *testing.T) {
	f := dropboxtest.New(t)
	f.AddFolder("/work")
	f.AddFile("/work/a.txt", []byte("alpha"))
	f.AddFile("/b.txt", []byte("beta"))
	c := newClient(t, f, "")

	files, err := c.ListTree(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, files, 3)

	cursor, err := c.StartPageToken(context.Background())
	require.NoError(t, err)

	// An unchanged stream yields no entries and reports caught-up.
	changes, _, end, err := c.Changes(context.Background(), cursor)
	require.NoError(t, err)
	require.Empty(t, changes)
	require.True(t, end)

	f.AddFile("/new.txt", []byte("new"))
	changes, _, _, err = c.Changes(context.Background(), cursor)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "new.txt", changes[0].File.Name)
	require.False(t, changes[0].File.Folder)
	require.Empty(t, changes[0].File.ParentID, "top-level parent is the account root")
}

func TestDeltaRemovalIsPathKeyed(t *testing.T) {
	f := dropboxtest.New(t)
	f.AddFile("/gone.txt", []byte("data"))
	c := newClient(t, f, "")
	cursor, err := c.StartPageToken(context.Background())
	require.NoError(t, err)

	f.Delete("/gone.txt")
	changes, _, _, err := c.Changes(context.Background(), cursor)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.True(t, changes[0].Removed)
	require.Empty(t, changes[0].FileID)
	require.Equal(t, []string{"gone.txt"}, []string{changes[0].Path})
}

func TestDeltaRenameOrdersEntriesBeforeRemovals(t *testing.T) {
	f := dropboxtest.New(t)
	f.AddFile("/old.txt", []byte("data"))
	c := newClient(t, f, "")
	cursor, err := c.StartPageToken(context.Background())
	require.NoError(t, err)

	f.Rename("/old.txt", "/new.txt")
	changes, _, _, err := c.Changes(context.Background(), cursor)
	require.NoError(t, err)
	require.Len(t, changes, 2)
	require.False(t, changes[0].Removed, "the new entry must come first")
	require.Equal(t, "new.txt", changes[0].File.Name)
	require.True(t, changes[1].Removed)
	require.Equal(t, "old.txt", changes[1].Path)
}

func TestScopedFolderRelativizesPaths(t *testing.T) {
	f := dropboxtest.New(t)
	f.AddFolder("/keep")
	f.AddFile("/keep/in.txt", []byte("in"))
	f.AddFile("/out.txt", []byte("out"))
	c := newClient(t, f, "/keep")

	root, err := c.GetFile(context.Background(), "/keep")
	require.NoError(t, err)
	files, err := c.ListTree(context.Background(), root.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "in.txt", files[0].Name)

	cursor, err := c.StartPageToken(context.Background())
	require.NoError(t, err)
	f.Delete("/keep/in.txt")
	changes, _, _, err := c.Changes(context.Background(), cursor)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	require.Equal(t, "in.txt", changes[0].Path, "removal paths are relative to the sync root")
}

func TestStaleCursorMapsToSentinel(t *testing.T) {
	f := dropboxtest.New(t)
	c := newClient(t, f, "")
	// A cursor beyond the change stream simulates expiry; the fake
	// answers with the reset error.
	_, _, _, err := c.Changes(context.Background(), "9999")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrStaleChangeToken))
}

func TestExclusionAndExportPolicy(t *testing.T) {
	c := newClient(t, dropboxtest.New(t), "")
	require.True(t, c.Excluded("text/csv", []string{"text/csv"}))
	require.False(t, c.Excluded("", nil), "dropbox has no contentless formats")
	require.NoError(t, c.CheckExport(nil))
	require.Error(t, c.CheckExport(map[string]string{"anything": "text/plain"}))
	target, err := c.ExportMime("", map[string]string{"x": "y"})
	require.NoError(t, err)
	require.Empty(t, target)
}

func TestDownload(t *testing.T) {
	f := dropboxtest.New(t)
	f.AddFile("/a.txt", []byte("body"))
	c := newClient(t, f, "")
	body, err := c.Download(context.Background(), "/a.txt")
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, "body", string(data))
}
