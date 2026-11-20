package contenthash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContentHashEmpty(t *testing.T) {
	got, err := HashHex(strings.NewReader(""))
	require.NoError(t, err)
	// SHA-256 of the empty string, per the spec.
	require.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", got)
}

func TestContentHashSmall(t *testing.T) {
	// Files under 4 MB hash as SHA-256(SHA-256(content)).
	content := []byte("hello dropbox")
	want := sha256.Sum256(func() []byte {
		s := sha256.Sum256(content)
		return s[:]
	}())
	got, err := HashHex(bytes.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(want[:]), got)
}

func TestContentHashMultiBlock(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 4*1024*1024+17)
	b1 := sha256.Sum256(content[:4*1024*1024])
	b2 := sha256.Sum256(content[4*1024*1024:])
	want := sha256.Sum256(append(b1[:], b2[:]...))
	got, err := HashHex(bytes.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(want[:]), got)
}
