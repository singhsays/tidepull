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
	_, err := Get("dropbox")
	require.Error(t, err)
}

func TestNames(t *testing.T) {
	names := Names()
	require.NotEmpty(t, names)
	for i := 1; i < len(names); i++ {
		require.Less(t, names[i-1], names[i], "names not sorted: %v", names)
	}
}
