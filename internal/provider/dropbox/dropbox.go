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
// account_info.read names the authenticated account for the status page.
var Scopes = []string{"files.metadata.read", "files.content.read", "account_info.read"}

// AuthParams are the extra authorize-URL parameters Dropbox needs:
// offline access so the account gets a refresh token.
var AuthParams = []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("token_access_type", "offline")}

// Icon is the provider mark shown on the status page (simple-icons,
// CC0). Fill follows the status page's text color.
var Icon = `<svg role="img" viewBox="0 0 24 24" width="16" height="16" fill="currentColor" xmlns="http://www.w3.org/2000/svg" aria-hidden="true"><path d="M6 1.807L0 5.629l6 3.822 6.001-3.822L6 1.807zM18 1.807l-6 3.822 6 3.822 6-3.822-6-3.822zM0 13.274l6 3.822 6.001-3.822L6 9.452l-6 3.822zM18 9.452l-6 3.822 6 3.822 6-3.822-6-3.822zM6 18.371l6.001 3.822 6-3.822-6-3.822L6 18.371z"/></svg>`

// RemoteURL returns the Dropbox web-UI address of the synced folder.
func RemoteURL(folderID string) string {
	if folderID == "" {
		return "https://www.dropbox.com/home"
	}
	return "https://www.dropbox.com/home" + folderID
}
