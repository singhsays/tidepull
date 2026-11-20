// Package dropbox is the Dropbox provider: its OAuth spec and a client
// over the Dropbox API v2 (folder cursor change stream, tree listing,
// content download).
package dropbox

import (
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// Endpoint is Dropbox's OAuth2 endpoint.
var Endpoint = endpoints.Dropbox

// Scopes are the OAuth scopes requested for a Dropbox account.
var Scopes = []string{"files.metadata.read", "files.content.read"}

// AuthParams are the extra authorize-URL parameters Dropbox needs:
// offline access so the account gets a refresh token.
var AuthParams = []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("token_access_type", "offline")}
