// Package provider maps provider names to their OAuth endpoints and scopes.
package provider

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/oauth2"

	"github.com/singhsays/tidepull/internal/provider/dropbox"
	"github.com/singhsays/tidepull/internal/provider/google"
)

// Spec describes one supported cloud provider.
type Spec struct {
	// Name is the provider key used in config files.
	Name string
	// Endpoint is the provider's OAuth2 auth/token URLs.
	Endpoint oauth2.Endpoint
	// Scopes are the default OAuth scopes to request.
	Scopes []string
	// AuthParams are extra authorize-URL options the provider needs,
	// e.g. offline access for refresh tokens.
	AuthParams []oauth2.AuthCodeOption
}

// specs is the registry of supported providers.
var specs = map[string]Spec{
	"google": {
		Name: "google", Endpoint: google.Endpoint, Scopes: google.Scopes,
		AuthParams: []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("access_type", "offline")},
	},
	"dropbox": {
		Name: "dropbox", Endpoint: dropbox.Endpoint, Scopes: dropbox.Scopes,
		AuthParams: dropbox.AuthParams,
	},
}

// Get returns the spec for a provider name.
func Get(name string) (Spec, error) {
	spec, ok := specs[name]
	if !ok {
		return Spec{}, fmt.Errorf("unknown provider %q (supported: %s)", name, strings.Join(Names(), ", "))
	}
	return spec, nil
}

// Names lists the supported providers.
func Names() []string {
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
