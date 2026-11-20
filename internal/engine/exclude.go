package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/singhsays/tidepull/internal/config"
	"github.com/singhsays/tidepull/internal/state"
)

// metaPolicyHash is the meta key holding the hash of the account's
// exclusion and export configuration.
const metaPolicyHash = "policy_hash"

// excluded reports whether the engine must not sync the file, asking
// the provider for its exclusion policy; a MIME type the export
// configuration enables is never excluded. Folders are structural and
// never excluded.
func (r *resolver) excluded(acct config.Account, f state.File) bool {
	if isFolderRow(f) {
		return false
	}
	if target, err := r.client.ExportMime(f.MimeType, acct.Export); err == nil && target != "" {
		return false
	}
	return r.client.Excluded(f.MimeType, acct.Exclude)
}

// policyHash fingerprints the account's exclusion and export
// configuration so a config change can trigger a full resync.
func policyHash(acct config.Account) string {
	entries := slices.Clone(acct.Exclude)
	for name, target := range acct.Export {
		entries = append(entries, "export "+name+"="+target)
	}
	slices.Sort(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\x00")))
	return hex.EncodeToString(sum[:])
}
