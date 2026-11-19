// Package auth stores provider credentials and runs OAuth 2.0 logins without
// third-party SDKs.
//
// Tokens live in a single JSON file inside a directory (mode 0600, written
// atomically). Two login flows are supported: the browser authorization-code
// flow with PKCE (RFC 7636) and the device-code flow (RFC 8628). Provider
// presets supply the endpoints; the package itself only knows the standards.
package auth

import (
	"time"
)

// Token kinds.
const (
	// KindOAuth marks a token obtained from an OAuth flow.
	KindOAuth = "oauth"
	// KindAPIKey marks a manually pasted API key.
	KindAPIKey = "api_key"
)

// expirySafety refreshes a token slightly before the provider's own deadline,
// mirroring the Rust implementation's 60-second margin.
const expirySafety = 60 * time.Second

// Token is one stored credential.
type Token struct {
	Provider string `json:"provider"`
	// Kind is KindOAuth or KindAPIKey.
	Kind string `json:"kind"`
	// Access is the bearer secret: an OAuth access token or an API key.
	Access string `json:"access,omitempty"`
	// Refresh is the OAuth refresh token, empty for API keys.
	Refresh string `json:"refresh,omitempty"`
	// ExpiresAt is the access-token deadline; the zero value means "no expiry".
	ExpiresAt time.Time `json:"-"`
	// Account optionally records which account the token belongs to.
	Account string `json:"account,omitempty"`
}

// Bearer returns the secret to send to the provider.
func (t Token) Bearer() string {
	if t.Access != "" {
		return t.Access
	}
	return t.Refresh
}

// Status describes how usable a stored token is right now.
type Status string

// Status values reported by Store.Status.
const (
	StatusValid    Status = "valid"
	StatusExpiring Status = "expiring"
	StatusExpired  Status = "expired"
	StatusMissing  Status = "missing"
)

// String implements fmt.Stringer.
func (s Status) String() string { return string(s) }

// expirySkew is the window in which a still-valid token is reported as
// expiring, so callers can refresh ahead of time.
const expirySkew = 5 * time.Minute
