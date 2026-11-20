package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPromptBearerSchemeIsCaseInsensitive guards the HTTP auth boundary: per
// RFC 7235 the authentication scheme is case-insensitive, so a client sending
// "Authorization: bearer TOKEN" (or any mixed case) must authenticate exactly
// like the canonical "Bearer TOKEN". A case-sensitive string prefix rejects
// those clients with a spurious 401.
// TestPromptBearerTokenStaysCaseSensitive ensures case folding applies only to
// the scheme, never to the credential: "bearer SECRET" still fails when the
// configured token uses different case, and a non-bearer scheme is rejected.
func TestPromptBearerTokenStaysCaseSensitive(t *testing.T) {
	h := NewHandler(Config{Token: "s3cret", Runner: &fakeRunner{reply: "hi"}})
	cases := []struct {
		name   string
		header string
	}{
		{"wrong token case", "bearer S3CRET"},
		{"non bearer scheme", "Basic s3cret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
			req.Header.Set("Authorization", tc.header)
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 for %q", rec.Code, tc.header)
			}
		})
	}
}

func TestPromptBearerSchemeIsCaseInsensitive(t *testing.T) {
	h := NewHandler(Config{Token: "s3cret", Runner: &fakeRunner{reply: "hi"}})
	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "BeArEr"} {
		t.Run(scheme, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/prompt", strings.NewReader(`{"text":"hi"}`))
			req.Header.Set("Authorization", scheme+" s3cret")
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 for scheme %q (body %q)", rec.Code, scheme, rec.Body.String())
			}
		})
	}
}
