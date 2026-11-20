package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
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
	if err := db.TokenSave("photos", want); err != nil {
		t.Fatal(err)
	}
	got, err := db.TokenLoad("photos")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "at" || got.RefreshToken != "rt" || got.TokenType != "Bearer" || got.Expiry.IsZero() {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestTokenOverwrite(t *testing.T) {
	db := open(t)
	if err := db.TokenSave("photos", &oauth2.Token{AccessToken: "old", RefreshToken: "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.TokenSave("photos", &oauth2.Token{AccessToken: "new", RefreshToken: "r2"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.TokenLoad("photos")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new" || got.RefreshToken != "r2" {
		t.Errorf("overwrite failed: %+v", got)
	}
}

func TestTokenLoadMissing(t *testing.T) {
	db := open(t)
	if _, err := db.TokenLoad("nosuch"); !errors.Is(err, ErrNoToken) {
		t.Errorf("err = %v, want ErrNoToken", err)
	}
}

func TestOpenTwiceMigratesIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.TokenSave("photos", &oauth2.Token{AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	got, err := second.TokenLoad("photos")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "at" {
		t.Errorf("token lost across open: %+v", got)
	}
}
