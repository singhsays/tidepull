package provider

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetGoogle(t *testing.T) {
	spec, err := Get("google")
	require.NoError(t, err)
	require.Equal(t, "google", spec.Name)
	require.NotEmpty(t, spec.Endpoint.AuthURL)
	require.NotEmpty(t, spec.Endpoint.TokenURL)
	require.NotEmpty(t, spec.Scopes)
}

func TestGetUnknown(t *testing.T) {
	_, err := Get("bogus")
	require.Error(t, err)
}

func TestGetDropbox(t *testing.T) {
	spec, err := Get("dropbox")
	require.NoError(t, err)
	require.Equal(t, "dropbox", spec.Name)
	require.NotEmpty(t, spec.Endpoint.AuthURL)
	require.Contains(t, spec.Scopes, "files.metadata.read")
	require.Contains(t, spec.Scopes, "files.content.read")
	require.NotEmpty(t, spec.AuthParams, "dropbox needs offline access for refresh tokens")
}

func TestNames(t *testing.T) {
	names := Names()
	require.NotEmpty(t, names)
	for i := 1; i < len(names); i++ {
		require.Less(t, names[i-1], names[i], "names not sorted: %v", names)
	}
}
