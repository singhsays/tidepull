// Package state persists tidepull's sync state and account tokens the database.
package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2"
	_ "modernc.org/sqlite"

	"github.com/adrg/xdg"
)

// ErrNoToken is returned when an account has no stored token.
var ErrNoToken = errors.New("no stored token")

// DB is the open state database.
type DB struct {
	db *sql.DB
}

// DefaultPath returns the database path, creating its directory.
func DefaultPath() (string, error) {
	return xdg.DataFile("tidepull/state.db")
}

// Open opens the database at path and applies migrations.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &DB{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// migrations are applied in order. Statements are idempotent.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS tokens (
		account    TEXT PRIMARY KEY,
		token      BLOB NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS page_tokens (
		account    TEXT PRIMARY KEY,
		page_token TEXT NOT NULL
	);`,
	`CREATE TABLE IF NOT EXISTS files (
		account   TEXT NOT NULL,
		file_id   TEXT NOT NULL,
		parent_id TEXT NOT NULL DEFAULT '',
		name      TEXT NOT NULL,
		mime_type TEXT NOT NULL,
		md5       TEXT NOT NULL DEFAULT '',
		size      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (account, file_id)
	);`,
}

func (s *DB) migrate() error {
	if _, err := s.db.Exec(`PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;`); err != nil {
		return err
	}
	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return err
		}
	}
	return nil
}

// TokenSave stores the account's token.
func (s *DB) TokenSave(account string, tok *oauth2.Token) error {
	raw, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO tokens (account, token, updated_at) VALUES (?, ?, ?)
ON CONFLICT(account) DO UPDATE SET token = excluded.token, updated_at = excluded.updated_at`,
		account, raw, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("token save for %q: %w", account, err)
	}
	return nil
}

// TokenLoad reads the account's token, returning ErrNoToken when absent.
func (s *DB) TokenLoad(account string) (*oauth2.Token, error) {
	var raw []byte
	err := s.db.QueryRow(`SELECT token FROM tokens WHERE account = ?`, account).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w for account %q (run \"tidepull auth login %s\")", ErrNoToken, account, account)
	}
	if err != nil {
		return nil, err
	}
	tok := &oauth2.Token{}
	if err := json.Unmarshal(raw, tok); err != nil {
		return nil, fmt.Errorf("token for %q: %w", account, err)
	}
	return tok, nil
}

// Close closes the database.
func (s *DB) Close() error {
	return s.db.Close()
}

// PageTokenSave stores the account's change-stream page token.
func (s *DB) PageTokenSave(account, tok string) error {
	_, err := s.db.Exec(`
INSERT INTO page_tokens (account, page_token) VALUES (?, ?)
ON CONFLICT(account) DO UPDATE SET page_token = excluded.page_token`, account, tok)
	if err != nil {
		return fmt.Errorf("page token save for %q: %w", account, err)
	}
	return nil
}

// PageTokenLoad reads the account's page token, "" when absent.
func (s *DB) PageTokenLoad(account string) (string, error) {
	var tok string
	err := s.db.QueryRow(`SELECT page_token FROM page_tokens WHERE account = ?`, account).Scan(&tok)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return tok, err
}

// File is one known remote file or folder in the account's tree.
type File struct {
	ID       string
	ParentID string
	Name     string
	MimeType string
	MD5      string
	Size     int64
}

// FileUpsert inserts or updates a file row.
func (s *DB) FileUpsert(account string, f File) error {
	_, err := s.db.Exec(`
INSERT INTO files (account, file_id, parent_id, name, mime_type, md5, size)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account, file_id) DO UPDATE SET
	parent_id = excluded.parent_id,
	name = excluded.name,
	mime_type = excluded.mime_type,
	md5 = excluded.md5,
	size = excluded.size`,
		account, f.ID, f.ParentID, f.Name, f.MimeType, f.MD5, f.Size)
	if err != nil {
		return fmt.Errorf("file upsert for %q: %w", f.ID, err)
	}
	return nil
}

// FileDelete removes a file row.
func (s *DB) FileDelete(account, fileID string) error {
	_, err := s.db.Exec(`DELETE FROM files WHERE account = ? AND file_id = ?`, account, fileID)
	return err
}

// FileDeleteAll removes every file row for the account (full resync).
func (s *DB) FileDeleteAll(account string) error {
	_, err := s.db.Exec(`DELETE FROM files WHERE account = ?`, account)
	return err
}

// Files returns all known files for the account, keyed by file ID.
func (s *DB) Files(account string) (map[string]File, error) {
	rows, err := s.db.Query(`
SELECT file_id, parent_id, name, mime_type, md5, size FROM files WHERE account = ?`, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := map[string]File{}
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Name, &f.MimeType, &f.MD5, &f.Size); err != nil {
			return nil, err
		}
		files[f.ID] = f
	}
	return files, rows.Err()
}
