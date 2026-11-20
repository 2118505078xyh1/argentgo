package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config describes one provider's OAuth endpoints and client identity. The
// presets in this package fill it in; a custom provider may set the fields
// directly.
type Config struct {
	// Provider is the id stored with the token (for example "openai").
	Provider string
	// ClientID is the public OAuth client identifier.
	ClientID string
	// ClientSecret is only needed by providers that are not public clients.
	ClientSecret string
	// AuthorizeURL is the browser authorization endpoint.
	AuthorizeURL string
	// TokenURL exchanges codes and refresh tokens.
	TokenURL string
	// DeviceURL starts a device-code flow (RFC 8628).
	DeviceURL string
	// RedirectURI must match the loopback listener used for the callback.
	RedirectURI string
	// Scopes are joined with spaces in requests.
	Scopes []string
	// Audience is an optional extra authorize parameter (Auth0 style).
	Audience string
	// Extra adds provider-specific authorize parameters.
	Extra map[string]string
	// HTTPClient overrides the default client (useful in tests).
	HTTPClient *http.Client
}

const tokenBodyLimit = 1 << 20

// PKCE returns a fresh RFC 7636 verifier and its S256 challenge.
func PKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: generate the PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	return verifier, S256(verifier), nil
}

// S256 is the base64url-encoded SHA-256 of verifier, the code_challenge value.
func S256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidatePKCE reports whether verifier produces challenge under method, using
// the code_challenge_method values defined by RFC 7636 section 4.2:
//
//   - "S256": challenge must equal BASE64URL-ENCODE(SHA256(ASCII(verifier))),
//     i.e. the value returned by S256.
//   - "plain": challenge must equal verifier verbatim. An empty method is
//     treated as "plain", which is the RFC 7636 default when the parameter is
//     absent from a request.
//
// Any other method is unknown and yields false, even if the strings would
// match under a known method. The verifier/challenge comparison is
// constant-time. ValidatePKCE is a pure function: it performs no I/O and never
// panics, so callers can use it to diagnose a broken PKCE pair before sending
// it to a provider.
func ValidatePKCE(verifier, challenge, method string) bool {
	var want string
	switch method {
	case "", "plain":
		want = verifier
	case "S256":
		want = S256(verifier)
	default:
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

// AuthorizeURL builds the browser URL for the authorization-code flow.
func AuthorizeURL(cfg Config, state, challenge string) (string, error) {
	if cfg.AuthorizeURL == "" {
		return "", errors.New("auth: config has no authorize endpoint")
	}
	if cfg.ClientID == "" {
		return "", errors.New("auth: config has no client id")
	}
	if challenge == "" {
		return "", errors.New("auth: missing PKCE challenge")
	}
	parsed, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("auth: bad authorize endpoint: %w", err)
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	if cfg.RedirectURI != "" {
		query.Set("redirect_uri", cfg.RedirectURI)
	}
	if len(cfg.Scopes) > 0 {
		query.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if cfg.Audience != "" {
		query.Set("audience", cfg.Audience)
	}
	for key, value := range cfg.Extra {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Scope            string `json:"scope"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// tokenError is a structured OAuth error response. Device polling inspects the
// code, so the endpoint status travels with the message.
type tokenError struct {
	Status      int
	Code        string
	Description string
}

func (e *tokenError) Error() string {
	detail := e.Code
	if e.Description != "" {
		detail = e.Code + ": " + e.Description
	}
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	return fmt.Sprintf("auth: token endpoint returned HTTP %d (%s)", e.Status, detail)
}

func (c Config) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Exchange swaps an authorization code for tokens.
func Exchange(ctx context.Context, cfg Config, code, verifier string) (Token, error) {
	if code == "" {
		return Token{}, errors.New("auth: missing authorization code")
	}
	if verifier == "" {
		return Token{}, errors.New("auth: missing PKCE verifier")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", cfg.ClientID)
	form.Set("code_verifier", verifier)
	if cfg.RedirectURI != "" {
		form.Set("redirect_uri", cfg.RedirectURI)
	}
	response, err := postToken(ctx, cfg, form)
	if err != nil {
		return Token{}, err
	}
	return tokenFromResponse(cfg, response, Token{})
}

// Refresh exchanges a refresh token for a new access token. The previous token
// supplies the provider, kind, account and refresh-token fallback.
func Refresh(ctx context.Context, cfg Config, token Token) (Token, error) {
	if token.Refresh == "" {
		return Token{}, errors.New("auth: token has no refresh token")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.Refresh)
	form.Set("client_id", cfg.ClientID)
	response, err := postToken(ctx, cfg, form)
	if err != nil {
		return Token{}, err
	}
	return tokenFromResponse(cfg, response, token)
}

func postToken(ctx context.Context, cfg Config, form url.Values) (tokenResponse, error) {
	var empty tokenResponse
	if cfg.TokenURL == "" {
		return empty, errors.New("auth: config has no token endpoint")
	}
	// Confidential clients authenticate at the token endpoint with their
	// secret (RFC 6749 sec 2.3.1). Public PKCE clients leave it empty, so no
	// parameter is added for them. Doing this here covers code exchange,
	// refresh and the device grant in one place.
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return empty, fmt.Errorf("auth: build the token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := cfg.httpClient().Do(request)
	if err != nil {
		return empty, fmt.Errorf("auth: token endpoint: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, tokenBodyLimit))
	if err != nil {
		return empty, fmt.Errorf("auth: read the token response: %w", err)
	}
	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		if response.StatusCode/100 != 2 {
			return empty, &tokenError{Status: response.StatusCode}
		}
		return empty, fmt.Errorf("auth: token endpoint returned HTTP %d with a non-JSON body", response.StatusCode)
	}
	if response.StatusCode/100 != 2 || parsed.Error != "" {
		return parsed, &tokenError{
			Status:      response.StatusCode,
			Code:        parsed.Error,
			Description: parsed.ErrorDescription,
		}
	}
	return parsed, nil
}

// tokenFromResponse converts the wire response into a stored token. expires_in
// is shortened by expirySafety so refresh happens before the provider's own
// deadline.
func tokenFromResponse(cfg Config, response tokenResponse, previous Token) (Token, error) {
	if response.AccessToken == "" {
		return Token{}, errors.New("auth: token endpoint returned no access_token")
	}
	token := Token{
		Provider: cfg.Provider,
		Kind:     KindOAuth,
		Access:   response.AccessToken,
		Refresh:  response.RefreshToken,
		Account:  previous.Account,
	}
	if token.Provider == "" {
		token.Provider = previous.Provider
	}
	if token.Refresh == "" {
		token.Refresh = previous.Refresh
	}
	if response.ExpiresIn > 0 {
		lifetime := time.Duration(response.ExpiresIn) * time.Second
		if lifetime > expirySafety {
			lifetime -= expirySafety
		} else {
			lifetime = 0
		}
		token.ExpiresAt = time.Now().Add(lifetime).Truncate(time.Second)
	}
	return token, nil
}

// ErrStateMismatch is returned when the provider echoes an unexpected state,
// which means the callback did not belong to this login attempt.
var ErrStateMismatch = errors.New("auth: oauth state mismatch")

// ErrNoAuthorizationCode is returned when the redirect carries the expected
// state but neither an authorization code nor an error. Such a callback cannot
// complete the login and must never be reported as a success.
var ErrNoAuthorizationCode = errors.New("auth: oauth callback has no authorization code")

// Callback is a loopback listener that receives one OAuth redirect.
type Callback struct {
	listener net.Listener
}

// Listen binds addr (empty means 127.0.0.1:0) and returns the pending callback.
func Listen(addr string) (*Callback, error) {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("auth: bind the OAuth callback: %w", err)
	}
	return &Callback{listener: listener}, nil
}

// Addr is the bound loopback address, for example 127.0.0.1:51234.
func (c *Callback) Addr() string { return c.listener.Addr().String() }

// URL is the redirect URI to hand to the provider.
func (c *Callback) URL() string { return "http://" + c.Addr() + "/callback" }

// Wait serves the redirect and returns the authorization code. Every response
// is a small HTML page so the browser shows something friendly. When state is
// non-empty it must match the callback's state parameter.
func (c *Callback) Wait(ctx context.Context, state string) (string, error) {
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	deliver := func(value result) { once.Do(func() { done <- value }) }

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("code") == "" && query.Get("error") == "" && query.Get("state") == "" {
			writePage(writer, http.StatusNotFound, "Not found",
				"Waiting for the OAuth callback on this port.")
			return
		}
		if state != "" && query.Get("state") != state {
			writePage(writer, http.StatusBadRequest, "Authorization failed",
				"The state parameter does not match this login attempt.")
			deliver(result{err: ErrStateMismatch})
			return
		}
		if failure := query.Get("error"); failure != "" {
			message := query.Get("error_description")
			if message == "" {
				message = failure
			}
			writePage(writer, http.StatusBadRequest, "Authorization failed", message)
			deliver(result{err: fmt.Errorf("auth: authorization failed: %s", message)})
			return
		}
		code := query.Get("code")
		if code == "" {
			// A redirect with the right state but no code (and no error) is
			// not a completed login. Treat it as a failure so callers never
			// receive an empty code together with a nil error, and the browser
			// is not told the authorization succeeded when it did not.
			writePage(writer, http.StatusBadRequest, "Authorization failed",
				"The authorization server did not return an authorization code.")
			deliver(result{err: ErrNoAuthorizationCode})
			return
		}
		writePage(writer, http.StatusOK, "Authorization complete",
			"You can close this window and return to argent.")
		deliver(result{code: code})
	})

	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(c.listener) }()
	defer server.Close()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-done:
		return value.code, value.err
	}
}

// Close releases the listener, cancelling a pending Wait.
func (c *Callback) Close() error { return c.listener.Close() }

// WaitCallback binds addr, waits for the redirect and returns the code. It does
// not check the state parameter; use WaitCallbackState when the expected state
// is known (the usual case).
func WaitCallback(ctx context.Context, addr string) (string, error) {
	return WaitCallbackState(ctx, addr, "")
}

// WaitCallbackState binds addr and returns the code only when the redirect
// carries the expected state.
func WaitCallbackState(ctx context.Context, addr, state string) (string, error) {
	callback, err := Listen(addr)
	if err != nil {
		return "", err
	}
	defer callback.Close()
	return callback.Wait(ctx, state)
}

func writePage(writer http.ResponseWriter, status int, title, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	fmt.Fprintf(writer, `<!doctype html><meta charset="utf-8"><title>argent</title>`+
		`<body style="font-family:system-ui;padding:3rem"><h1>%s</h1><p>%s</p>`,
		html.EscapeString(title), html.EscapeString(message))
}
