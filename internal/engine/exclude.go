package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/state"
)

// metaExcludeHash is the meta key holding the hash of the account's
// configured exclude list.
const metaExcludeHash = "exclude_hash"

// excluded reports whether the engine must not sync the file, asking
// the provider for its exclusion policy. Folders are structural and
// never excluded.
func (r *resolver) excluded(acct config.Account, f state.File) bool {
	if isFolderRow(f) {
		return false
	}
	return r.client.Excluded(f.MimeType, acct.Exclude)
}

// excludeHash fingerprints the account's configured exclude list so a
// config change can trigger a full resync.
func excludeHash(entries []string) string {
	sorted := slices.Clone(entries)
	slices.Sort(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(sum[:])
}
