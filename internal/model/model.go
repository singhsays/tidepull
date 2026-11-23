// Package model defines the provider-neutral data types shared by the
// pull engine and the providers.
package model

import "errors"

// File is the subset of file metadata the sync needs.
type File struct {
	ID           string
	ParentID     string
	Name         string
	MimeType     string
	Folder       bool
	Hash         string
	Size         int64
	ModifiedTime string
}

// Change is one entry from the provider's change stream. For removals
// the provider records by path rather than by ID, Path holds the path
// relative to the synced folder root.
type Change struct {
	FileID  string
	Path    string
	Removed bool
	File    *File
}

// AccountIdentity names the cloud account behind a token, as the
// provider reports it.
type AccountIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ErrStaleChangeToken reports that a change-stream page token is no
// longer valid and the stream must be restarted.
var ErrStaleChangeToken = errors.New("stale change token")
