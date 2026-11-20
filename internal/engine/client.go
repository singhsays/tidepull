package engine

import (
	"context"
	"hash"
	"io"

	"github.com/singhsays/tidepull/internal/model"
)

// Client is the provider surface the pull engine consumes: a change
// stream, tree listing, metadata fetches, content downloads and the
// provider's exclusion and export policy.
type Client interface {
	StartPageToken(ctx context.Context) (string, error)
	Changes(ctx context.Context, pageToken string) ([]*model.Change, string, bool, error)
	ListTree(ctx context.Context, root string) ([]*model.File, error)
	GetFile(ctx context.Context, fileID string) (*model.File, error)
	Download(ctx context.Context, fileID string) (io.ReadCloser, error)
	// NewContentHash returns a hash.Hash for the provider's content hash,
	// used to verify downloaded content.
	NewContentHash() hash.Hash
	// Excluded reports whether the provider cannot sync the MIME type,
	// such as native document formats without downloadable content,
	// including the account-configured exclusions in extra.
	Excluded(mime string, extra []string) bool
	// CheckExport validates an account's export configuration up front;
	// the engine fails the cycle on error.
	CheckExport(cfg map[string]string) error
	// ExportMime reports the MIME type to export the source type as when
	// the account's export configuration enables it, "" when it does not.
	ExportMime(mime string, cfg map[string]string) (string, error)
	// Export opens the file's content rendered as the target MIME type
	// (for native document formats with no downloadable binary content).
	Export(ctx context.Context, fileID, targetMime string) (io.ReadCloser, error)
}
