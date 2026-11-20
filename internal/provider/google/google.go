// Package google provides the Google provider spec.
package google

import "golang.org/x/oauth2/endpoints"

// Endpoint is Google's OAuth2 endpoint.
var Endpoint = endpoints.Google

// Scopes are the OAuth scopes requested for a Google account. The drive
// scope covers both pull and later two-way sync.
var Scopes = []string{"https://www.googleapis.com/auth/drive"}
