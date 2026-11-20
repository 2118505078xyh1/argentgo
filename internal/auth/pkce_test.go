package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestS256MatchesTheRFC7636Vector(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := S256(verifier); got != challenge {
		t.Fatalf("S256 = %q, want %q", got, challenge)
	}
}

func TestPKCEProducesAUsableVerifier(t *testing.T) {
	verifier, challenge, err := PKCE()
	if err != nil {
		t.Fatalf("PKCE: %v", err)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("verifier length %d outside the RFC 7636 range", len(verifier))
	}
	if strings.ContainsAny(verifier, "=+/ ") {
		t.Fatalf("verifier is not url safe: %q", verifier)
	}
	if challenge != S256(verifier) {
		t.Fatal("challenge does not match the verifier")
	}
	other, _, err := PKCE()
	if err != nil {
		t.Fatalf("PKCE: %v", err)
	}
	if other == verifier {
		t.Fatal("two PKCE calls returned the same verifier")
	}
}

func TestAuthorizeURLCarriesThePKCEParameters(t *testing.T) {
	cfg := Config{
		Provider:     "demo",
		ClientID:     "client-1",
		AuthorizeURL: "https://example.invalid/authorize?prompt=consent",
		RedirectURI:  "http://127.0.0.1:8123/callback",
		Scopes:       []string{"openid", "profile"},
		Audience:     "https://api.example.invalid",
	}
	raw, err := AuthorizeURL(cfg, "state-1", "challenge-1")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Host != "example.invalid" || parsed.Path != "/authorize" {
		t.Fatalf("endpoint changed: %s", raw)
	}
	query := parsed.Query()
	want := map[string]string{
		"response_type":         "code",
		"client_id":             "client-1",
		"redirect_uri":          "http://127.0.0.1:8123/callback",
		"state":                 "state-1",
		"code_challenge":        "challenge-1",
		"code_challenge_method": "S256",
		"scope":                 "openid profile",
		"audience":              "https://api.example.invalid",
		"prompt":                "consent",
	}
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Fatalf("%s = %q, want %q (%s)", key, got, value, raw)
		}
	}
}

// TestAuthorizeURLDoesNotLetExtraOverrideSecurityParameters guards the
// precedence between provider extras and the OAuth protocol parameters. An
// Extra entry named "state" or "code_challenge" must not replace the values
// passed to AuthorizeURL: Callback.Wait validates against the former and
// Exchange proves possession of the verifier for the latter.
func TestAuthorizeURLDoesNotLetExtraOverrideSecurityParameters(t *testing.T) {
	cfg := Config{
		Provider:     "demo",
		ClientID:     "client-1",
		AuthorizeURL: "https://example.invalid/authorize?prompt=consent",
		RedirectURI:  "http://127.0.0.1:8123/callback",
		Scopes:       []string{"openid"},
		Audience:     "https://api.example.invalid",
		Extra: map[string]string{
			"state":                 "attacker-state",
			"code_challenge":        "attacker-challenge",
			"code_challenge_method": "plain",
			"response_type":         "token",
			"client_id":             "attacker-client",
			"redirect_uri":          "http://attacker.invalid/cb",
			"scope":                 "evil",
			"audience":              "evil",
			"access_type":           "offline", // a legitimate extra must survive
		},
	}
	raw, err := AuthorizeURL(cfg, "state-1", "challenge-1")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]string{
		"state":                 "state-1",
		"code_challenge":        "challenge-1",
		"code_challenge_method": "S256",
		"response_type":         "code",
		"client_id":             "client-1",
		"redirect_uri":          cfg.RedirectURI,
		"scope":                 "openid",
		"audience":              cfg.Audience,
		"access_type":           "offline",
		"prompt":                "consent",
	}
	for key, value := range want {
		if got := parsed.Query().Get(key); got != value {
			t.Fatalf("%s = %q, want %q (%s)", key, got, value, raw)
		}
	}
}

func TestAuthorizeURLRejectsIncompleteConfig(t *testing.T) {
	cases := []Config{
		{ClientID: "c"},
		{AuthorizeURL: "https://example.invalid/authorize"},
		{AuthorizeURL: "https://example.invalid/authorize", ClientID: "c"},
	}
	for _, cfg := range cases {
		if _, err := AuthorizeURL(cfg, "s", ""); err == nil {
			t.Fatalf("AuthorizeURL(%+v) succeeded, want error", cfg)
		}
	}
}

func TestExchangePostsTheCodeVerifier(t *testing.T) {
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		form, _ = url.ParseQuery(string(body))
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"new","refresh_token":"next","expires_in":3600}`)
	}))
	defer server.Close()

	cfg := Config{
		Provider:    "demo",
		ClientID:    "client",
		TokenURL:    server.URL,
		RedirectURI: "http://127.0.0.1:1/callback",
	}
	token, err := Exchange(context.Background(), cfg, "code-1", "verifier-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	want := map[string]string{
		"grant_type":    "authorization_code",
		"code":          "code-1",
		"client_id":     "client",
		"code_verifier": "verifier-1",
		"redirect_uri":  "http://127.0.0.1:1/callback",
	}
	for key, value := range want {
		if got := form.Get(key); got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
	if token.Provider != "demo" || token.Kind != KindOAuth || token.Access != "new" || token.Refresh != "next" {
		t.Fatalf("token = %+v", token)
	}
	// The 60-second safety margin is applied to expires_in.
	left := time.Until(token.ExpiresAt)
	if left < 58*time.Minute || left > 60*time.Minute {
		t.Fatalf("expiry is %v from now, want about 59m", left)
	}
}

// TestExchangeKeepsShortLivedTokensUsable guards the expiry-safety margin: a
// credential whose lifetime is shorter than the safety window must not be born
// expired. The margin exists to trigger an early refresh, not to discard an
// otherwise valid token the moment it is issued.
func TestExchangeKeepsShortLivedTokensUsable(t *testing.T) {
	const lifetime = 30 * time.Second
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"short","expires_in":30}`)
	}))
	defer server.Close()

	cfg := Config{Provider: "demo", ClientID: "client", TokenURL: server.URL}
	token, err := Exchange(context.Background(), cfg, "code", "verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	left := time.Until(token.ExpiresAt)
	if left <= 0 {
		t.Fatalf("short-lived token is already expired at issuance (ExpiresAt=%v)", token.ExpiresAt)
	}
	if left > lifetime {
		t.Fatalf("expiry is %v from now, later than the provider's %v deadline", left, lifetime)
	}
	if left < lifetime/3 {
		t.Fatalf("expiry is %v from now, leaving too little of the %v lifetime", left, lifetime)
	}
}

func TestExchangeReportsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		io.WriteString(writer, `{"error":"invalid_grant","error_description":"code expired"}`)
	}))
	defer server.Close()

	cfg := Config{Provider: "demo", ClientID: "client", TokenURL: server.URL}
	_, err := Exchange(context.Background(), cfg, "code", "verifier")
	if err == nil {
		t.Fatal("Exchange succeeded, want an error")
	}
	var failure *tokenError
	if !errors.As(err, &failure) {
		t.Fatalf("error is not a tokenError: %v", err)
	}
	if failure.Code != "invalid_grant" || failure.Status != http.StatusBadRequest {
		t.Fatalf("tokenError = %+v", failure)
	}
	if !strings.Contains(err.Error(), "code expired") {
		t.Fatalf("error does not carry the description: %v", err)
	}
}

func TestRefreshKeepsTheOldRefreshToken(t *testing.T) {
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		form, _ = url.ParseQuery(string(body))
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"access_token":"fresh"}`)
	}))
	defer server.Close()

	cfg := Config{Provider: "demo", ClientID: "client", TokenURL: server.URL}
	previous := Token{Provider: "demo", Kind: KindOAuth, Access: "old", Refresh: "keep-me", Account: "me"}
	token, err := Refresh(context.Background(), cfg, previous)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "keep-me" {
		t.Fatalf("refresh form = %v", form)
	}
	if token.Access != "fresh" || token.Refresh != "keep-me" || token.Account != "me" {
		t.Fatalf("refreshed token = %+v", token)
	}
	if !token.ExpiresAt.IsZero() {
		t.Fatalf("token without expires_in has a deadline: %v", token.ExpiresAt)
	}
}

func TestRefreshWithoutARefreshTokenFails(t *testing.T) {
	if _, err := Refresh(context.Background(), Config{TokenURL: "https://example.invalid/x"}, Token{}); err == nil {
		t.Fatal("Refresh succeeded, want an error")
	}
}

// TestTokenRequestsSendClientSecretWhenConfigured guards the confidential-client
// path: a Config with a ClientSecret must authenticate every token-endpoint
// request, while public PKCE clients must not emit an empty client_secret.
func TestTokenRequestsSendClientSecretWhenConfigured(t *testing.T) {
	cases := []struct {
		name       string
		secret     string
		wantSecret string
	}{
		{name: "public client omits client_secret", secret: "", wantSecret: ""},
		{name: "confidential client sends client_secret", secret: "top-secret", wantSecret: "top-secret"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var forms []url.Values
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				form, _ := url.ParseQuery(string(body))
				forms = append(forms, form)
				writer.Header().Set("Content-Type", "application/json")
				io.WriteString(writer, `{"access_token":"tok","refresh_token":"ref"}`)
			}))
			defer server.Close()

			cfg := Config{
				Provider:     "demo",
				ClientID:     "client",
				ClientSecret: test.secret,
				TokenURL:     server.URL,
			}
			if _, err := Exchange(context.Background(), cfg, "code", "verifier"); err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			previous := Token{Provider: "demo", Kind: KindOAuth, Access: "old", Refresh: "keep"}
			if _, err := Refresh(context.Background(), cfg, previous); err != nil {
				t.Fatalf("Refresh: %v", err)
			}

			if len(forms) != 2 {
				t.Fatalf("got %d token requests, want 2", len(forms))
			}
			for index, form := range forms {
				if got := form.Get("client_secret"); got != test.wantSecret {
					t.Fatalf("request %d client_secret = %q, want %q (form: %v)", index, got, test.wantSecret, form)
				}
				_, present := form["client_secret"]
				if test.wantSecret == "" && present {
					t.Fatalf("request %d sent an empty client_secret: %v", index, form)
				}
			}
		})
	}
}

func TestCallbackChecksStateAndReturnsTheCode(t *testing.T) {
	callback, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer callback.Close()

	type outcome struct {
		code string
		err  error
	}
	results := make(chan outcome, 1)
	go func() {
		code, err := callback.Wait(context.Background(), "state-1")
		results <- outcome{code, err}
	}()

	response, err := http.Get(callback.URL() + "?code=abc&state=wrong")
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if !strings.Contains(string(body), "Authorization failed") {
		t.Fatalf("unexpected callback page: %s", body)
	}
	got := <-results
	if !errors.Is(got.err, ErrStateMismatch) {
		t.Fatalf("Wait error = %v, want ErrStateMismatch", got.err)
	}

	callback, err = Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer callback.Close()
	go func() {
		code, err := callback.Wait(context.Background(), "state-2")
		results <- outcome{code, err}
	}()
	response, err = http.Get(callback.URL() + "?code=xyz&state=state-2")
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if !strings.Contains(string(body), "Authorization complete") {
		t.Fatalf("unexpected callback page: %s", body)
	}
	got = <-results
	if got.err != nil || got.code != "xyz" {
		t.Fatalf("Wait = %q, %v; want xyz, nil", got.code, got.err)
	}
}

// TestCallbackRejectsRedirectWithoutCode guards the callback's success path: a
// redirect that carries the expected state but no code and no error must be
// reported as a failure, never as a successful login with an empty code.
func TestCallbackRejectsRedirectWithoutCode(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{name: "state only", query: "?state=state-1"},
		{name: "empty code", query: "?state=state-1&code="},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			callback, err := Listen("127.0.0.1:0")
			if err != nil {
				t.Fatalf("Listen: %v", err)
			}
			defer callback.Close()

			type outcome struct {
				code string
				err  error
			}
			results := make(chan outcome, 1)
			go func() {
				code, err := callback.Wait(context.Background(), "state-1")
				results <- outcome{code, err}
			}()

			response, err := http.Get(callback.URL() + test.query)
			if err != nil {
				t.Fatalf("callback request: %v", err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
			if strings.Contains(string(body), "Authorization complete") {
				t.Fatalf("callback falsely reported success: %s", body)
			}
			got := <-results
			if !errors.Is(got.err, ErrNoAuthorizationCode) {
				t.Fatalf("Wait error = %v, want ErrNoAuthorizationCode", got.err)
			}
			if got.code != "" {
				t.Fatalf("Wait code = %q, want empty", got.code)
			}
		})
	}
}

func TestCallbackHonoursContextCancellation(t *testing.T) {
	callback, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer callback.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := callback.Wait(ctx, "state"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}
}

// TestCallbackCloseCancelsAPendingWait guards the contract in Close's doc:
// closing the callback must unblock a Wait even when its context is never
// cancelled. Closing only the TCP listener leaves the HTTP server idle and the
// Wait stuck on its select forever.
func TestCallbackCloseCancelsAPendingWait(t *testing.T) {
	callback, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	type outcome struct {
		code string
		err  error
	}
	results := make(chan outcome, 1)
	go func() {
		code, err := callback.Wait(context.Background(), "state")
		results <- outcome{code, err}
	}()

	if err := callback.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case got := <-results:
		if !errors.Is(got.err, ErrCallbackClosed) {
			t.Fatalf("Wait error = %v, want ErrCallbackClosed", got.err)
		}
		if got.code != "" {
			t.Fatalf("Wait code = %q, want empty", got.code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after Close")
	}
}

func TestCallbackCloseIsIdempotent(t *testing.T) {
	callback, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := callback.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// A second Close must not panic (the closed channel may only be closed
	// once) and must not report a new error.
	if err := callback.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestWaitCallbackStateBindsAndReturns(t *testing.T) {
	addr := "127.0.0.1:0"
	// WaitCallbackState with a zero port cannot report the chosen address to the
	// caller, so bind explicitly to exercise the redirect path.
	callback, err := Listen(addr)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	url := callback.URL() + "?code=c&state=s"
	defer callback.Close()
	go func() {
		time.Sleep(10 * time.Millisecond)
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
		}
	}()
	code, err := callback.Wait(context.Background(), "s")
	if err != nil || code != "c" {
		t.Fatalf("Wait = %q, %v", code, err)
	}
}

func TestTokenJSONShapeStaysSecret(t *testing.T) {
	// Guards against silently renaming the wire fields the Rust store uses.
	token := Token{Provider: "p", Kind: KindOAuth, Access: "a", Refresh: "r"}
	encoded, err := json.Marshal(token)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"provider"`, `"kind"`, `"access"`, `"refresh"`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("token JSON lost %s: %s", field, encoded)
		}
	}
}
