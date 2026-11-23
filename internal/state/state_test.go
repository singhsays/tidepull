package state

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/stretchr/testify/require"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestTokenRoundTrip(t *testing.T) {
	db := open(t)
	want := &oauth2.Token{
		AccessToken:  "at",
		TokenType:    "Bearer",
		RefreshToken: "rt",
		Expiry:       time.Now().Add(time.Hour).Truncate(time.Second),
	}
	require.NoError(t, db.TokenSave("photos", want))
	got, err := db.TokenLoad("photos")
	require.NoError(t, err)
	require.Equal(t, "at", got.AccessToken)
	require.Equal(t, "rt", got.RefreshToken)
	require.Equal(t, "Bearer", got.TokenType)
	require.False(t, got.Expiry.IsZero())
}

func TestTokenOverwrite(t *testing.T) {
	db := open(t)
	require.NoError(t, db.TokenSave("photos", &oauth2.Token{AccessToken: "old", RefreshToken: "r1"}))
	require.NoError(t, db.TokenSave("photos", &oauth2.Token{AccessToken: "new", RefreshToken: "r2"}))
	got, err := db.TokenLoad("photos")
	require.NoError(t, err)
	require.Equal(t, "new", got.AccessToken)
	require.Equal(t, "r2", got.RefreshToken)
}

func TestTokenLoadMissing(t *testing.T) {
	db := open(t)
	_, err := db.TokenLoad("nosuch")
	require.ErrorIs(t, err, ErrNoToken)
}

func TestMetaRoundTrip(t *testing.T) {
	db := open(t)
	got, err := db.MetaLoad("photos", "exclude_hash")
	require.NoError(t, err)
	require.Empty(t, got)

	require.NoError(t, db.MetaSave("photos", "exclude_hash", "abc"))
	require.NoError(t, db.MetaSave("photos", "exclude_hash", "def"))
	got, err = db.MetaLoad("photos", "exclude_hash")
	require.NoError(t, err)
	require.Equal(t, "def", got)

	got, err = db.MetaLoad("other", "exclude_hash")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDSN(t *testing.T) {
	got := dsn(filepath.Join("db", "state.db"))
	require.Contains(t, got, "file:")
	require.Contains(t, got, "_pragma=busy_timeout(5000)")
	require.Contains(t, got, "_pragma=journal_mode(WAL)")
}

func TestOpenTwiceMigratesIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, first.TokenSave("photos", &oauth2.Token{AccessToken: "at"}))
	_ = first.Close()

	second, err := Open(path)
	require.NoError(t, err)
	defer func() { _ = second.Close() }()
	got, err := second.TokenLoad("photos")
	require.NoError(t, err)
	require.Equal(t, "at", got.AccessToken)
}

func TestConcurrentUse(t *testing.T) {
	db := open(t)
	// Widen the pool past the production setting of 1 so writers really
	// collide on separate connections.
	db.db.SetMaxOpenConns(8)
	require.NoError(t, db.TokenSave("photos", &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}))
	var mu sync.Mutex
	var errs []error
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			if err := db.TokenSave("photos", &oauth2.Token{AccessToken: "at", RefreshToken: "rt"}); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			if err := db.MetaSave("photos", "key", "value"); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			if err := db.PageTokenSave("photos", "tok"); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := db.TokenLoad("photos"); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			if _, err := db.MetaLoad("photos", "key"); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
			if _, err := db.PageTokenLoad("photos"); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Empty(t, errs)
}
