// Package contenthash implements Dropbox's content hash: the file
// splits into 4 MB blocks, each block hashes with SHA-256, and the
// concatenation of the block hashes hashes with SHA-256 again. An
// empty file has no blocks, hashing to SHA-256 of nothing.
package contenthash

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// BlockSize is the Dropbox content-hash block size: 4 MB.
const BlockSize = 4 * 1024 * 1024

// hasher computes the content hash incrementally.
type hasher struct {
	blocks  [][]byte
	current []byte
}

// New returns a hash.Hash computing Dropbox's content hash.
func New() hash.Hash {
	return &hasher{}
}

func (h *hasher) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		space := BlockSize - len(h.current)
		n := min(space, len(p))
		h.current = append(h.current, p[:n]...)
		p = p[n:]
		written += n
		if len(h.current) == BlockSize {
			sum := sha256.Sum256(h.current)
			h.blocks = append(h.blocks, sum[:])
			h.current = nil
		}
	}
	return written, nil
}

func (h *hasher) Sum(b []byte) []byte {
	var all []byte
	for _, block := range h.blocks {
		all = append(all, block...)
	}
	// The trailing partial block is hashed as a block too. An empty
	// file has no blocks at all, hashing to SHA-256 of nothing.
	if h.current != nil {
		sum := sha256.Sum256(h.current)
		all = append(all, sum[:]...)
	}
	sum := sha256.Sum256(all)
	return append(b, sum[:]...)
}

func (h *hasher) Reset() {
	h.blocks = nil
	h.current = nil
}

func (h *hasher) Size() int { return sha256.Size }

func (h *hasher) BlockSize() int { return BlockSize }

// HashHex hashes r with Dropbox's content-hash algorithm and returns
// the hex digest.
func HashHex(r io.Reader) (string, error) {
	h := New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
