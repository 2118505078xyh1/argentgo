package security

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactAuthorizationSchemes(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "basic auth in curl command",
			in:      `curl -H "Authorization: Basic dXNlcjpwYXNz" https://x`,
			absent:  []string{"Basic", "dXNlcjpwYXNz"},
			present: []string{"Authorization:", "https://x", "[redacted:auth header]"},
		},
		{
			name:    "basic auth base64 padding",
			in:      `Authorization: Basic dXNlcjpwYXNzKysvPT0=`,
			absent:  []string{"dXNlcjpwYXNzKysvPT0="},
			present: []string{"[redacted:auth header]"},
		},
		{
			name:    "bearer auth",
			in:      "Authorization: Bearer abc.def.ghi",
			absent:  []string{"Bearer", "abc.def.ghi"},
			present: []string{"Authorization:", "[redacted:auth header]"},
		},
		{
			name:    "mac scheme",
			in:      "Authorization: Mac token=abc123",
			absent:  []string{"Mac", "abc123"},
			present: []string{"Authorization:", "[redacted:auth header]"},
		},
		{
			name:    "unknown scheme",
			in:      "Authorization: Something opaque-credential-42",
			absent:  []string{"Something", "opaque-credential-42"},
			present: []string{"[redacted:auth header]"},
		},
		{
			name:    "lowercase header name",
			in:      "authorization: Basic dXNlcjpwYXNz",
			absent:  []string{"dXNlcjpwYXNz"},
			present: []string{"[redacted:auth header]"},
		},
		{
			name:    "standalone bearer keeps scheme",
			in:      "retry with Bearer abc.def.ghi now",
			absent:  []string{"abc.def.ghi"},
			present: []string{"Bearer", "[redacted:bearer token]"},
		},
	}
	for _, tc := range cases {
		out := Redact(tc.in)
		for _, secret := range tc.absent {
			if strings.Contains(out, secret) {
				t.Errorf("%s: %q leaked %q", tc.name, out, secret)
			}
		}
		for _, marker := range tc.present {
			if !strings.Contains(out, marker) {
				t.Errorf("%s: %q lost %q", tc.name, out, marker)
			}
		}
	}
}

func TestRedactURLUserinfo(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "https userinfo",
			in:      "https://user:password@example.com/repo",
			absent:  []string{"user", "password"},
			present: []string{"https://", "example.com/repo", "[redacted:url credentials]"},
		},
		{
			name:    "credential with port and query",
			in:      "postgres://admin:s3cr3t@db.internal:5432/app?ssl=true",
			absent:  []string{"admin", "s3cr3t"},
			present: []string{"postgres://", "db.internal:5432/app?ssl=true"},
		},
		{
			name:    "percent encoded credential",
			in:      "https://user:p%40ss%3Aword@host.example/path",
			absent:  []string{"p%40ss%3Aword"},
			present: []string{"host.example/path"},
		},
		{
			name:    "credential with unescaped at sign",
			in:      "https://user:p@ss@host.example/x",
			absent:  []string{"user:p@ss"},
			present: []string{"host.example/x"},
		},
	}
	for _, tc := range cases {
		out := Redact(tc.in)
		for _, secret := range tc.absent {
			if strings.Contains(out, secret) {
				t.Errorf("%s: %q leaked %q", tc.name, out, secret)
			}
		}
		for _, marker := range tc.present {
			if !strings.Contains(out, marker) {
				t.Errorf("%s: %q lost %q", tc.name, out, marker)
			}
		}
	}
}

func TestRedactLeavesOrdinaryTextAndURLsAlone(t *testing.T) {
	cases := []string{
		"read the file src/main.go and count the lines",
		"see https://example.com/repo for the docs",
		"mail user@example.com when the build finishes",
		"the local port 8080 is listening on localhost:8080",
		"Bearer ",
		"https://example.com/already@public/path",
	}
	for _, in := range cases {
		if got := Redact(in); got != in {
			t.Errorf("Redact(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactUTF8AroundCredentials(t *testing.T) {
	in := "请求 Authorization: Basic dXNlcjpwYXNz 完成 https://user:pw@example.com/x 结束"
	out := Redact(in)
	for _, secret := range []string{"dXNlcjpwYXNz", "user:pw"} {
		if strings.Contains(out, secret) {
			t.Fatalf("credential %q leaked: %q", secret, out)
		}
	}
	if !utf8.ValidString(out) {
		t.Fatalf("redacted output is not valid UTF-8: %q", out)
	}
	for _, marker := range []string{"请求", "完成", "结束", "example.com/x"} {
		if !strings.Contains(out, marker) {
			t.Fatalf("UTF-8 text %q lost: %q", marker, out)
		}
	}
}

func TestRedactRowScrubsURLAndAuthWithoutMovingANSI(t *testing.T) {
	line := []byte("\x1b[32mOK\x1b[0m Authorization: Basic dXNlcjpwYXNz https://user:pw@example.com/x \x1b[1mend\x1b[0m")
	before := ansiSequences(line)
	out := RedactRow(line)
	for _, secret := range []string{"dXNlcjpwYXNz", "user:pw"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Fatalf("credential %q leaked into row: %q", secret, out)
		}
	}
	if got := ansiSequences(out); !reflectDeepEqualStrings(got, before) {
		t.Fatalf("ANSI sequences changed:\n got %q\nwant %q", got, before)
	}
	if !bytes.Contains(out, []byte("example.com/x")) {
		t.Fatalf("host lost: %q", out)
	}
}

func reflectDeepEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRedactJSONScrubsAuthorizationAndURL(t *testing.T) {
	in := []byte(`{"command":"curl -H \"Authorization: Basic dXNlcjpwYXNz\" https://user:pw@example.com/x","ok":true}`)
	out := RedactJSON(in)
	for _, secret := range []string{"dXNlcjpwYXNz", "user:pw"} {
		if strings.Contains(out, secret) {
			t.Fatalf("credential %q leaked from JSON: %q", secret, out)
		}
	}
	if !strings.Contains(out, "example.com/x") || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("JSON lost non-secret content: %q", out)
	}
}

// TestRedactAuthorizationWithoutSpace covers the HTTP-valid spelling with no
// OWS after the header colon. The headerPrefixes table used to only match
// "Authorization: " (with a trailing space), so `Authorization:Basic <b64>`
// leaked its credential: a bare base64 token is not credential-shaped and the
// spaced needles never matched. Both the raw-text and JSON/row paths must be
// safe because audit reasons and terminal output use them.
func TestRedactAuthorizationWithoutSpace(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "basic auth without space",
			in:      `Authorization:Basic dXNlcjpwYXNz`,
			absent:  []string{"Basic", "dXNlcjpwYXNz"},
			present: []string{"Authorization:", placeholder("auth header")},
		},
		{
			name:    "lowercase header without space",
			in:      `authorization:Basic dXNlcjpwYXNzKysvPT0=`,
			absent:  []string{"Basic", "dXNlcjpwYXNzKysvPT0="},
			present: []string{placeholder("auth header")},
		},
		{
			name:    "inside a curl header argument",
			in:      `curl -H 'Authorization:Basic dXNlcjpwYXNz' https://example.com`,
			absent:  []string{"Basic", "dXNlcjpwYXNz"},
			present: []string{"curl -H", "https://example.com", placeholder("auth header")},
		},
		{
			name:    "bearer scheme without space",
			in:      `Authorization:Bearer abc.def.ghi`,
			absent:  []string{"Bearer", "abc.def.ghi"},
			present: []string{placeholder("auth header")},
		},
		{
			name:    "unknown scheme without space",
			in:      `Authorization:Mac token=abc123`,
			absent:  []string{"Mac", "abc123"},
			present: []string{placeholder("auth header")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Redact(tc.in)
			for _, secret := range tc.absent {
				if strings.Contains(out, secret) {
					t.Errorf("Redact(%q) = %q, leaked %q", tc.in, out, secret)
				}
			}
			for _, marker := range tc.present {
				if !strings.Contains(out, marker) {
					t.Errorf("Redact(%q) = %q, lost %q", tc.in, out, marker)
				}
			}
		})
	}
}

// TestRedactAuthorizationNoSpaceStructuredPaths checks that the no-space
// spelling is scrubbed on the row and JSON paths too, and that the existing
// spaced spelling still produces a single placeholder (the two needles must
// not stack into a double redaction).
func TestRedactAuthorizationNoSpaceStructuredPaths(t *testing.T) {
	spaced := Redact(`Authorization: Basic dXNlcjpwYXNz`)
	if want := "Authorization: " + placeholder("auth header"); spaced != want {
		t.Fatalf("spaced header = %q, want %q", spaced, want)
	}

	line := []byte("\x1b[32mOK\x1b[0m Authorization:Basic dXNlcjpwYXNz \x1b[1mend\x1b[0m")
	before := ansiSequences(line)
	out := RedactRow(line)
	for _, secret := range []string{"Basic", "dXNlcjpwYXNz"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Fatalf("credential %q leaked into row: %q", secret, out)
		}
	}
	if got := ansiSequences(out); !reflectDeepEqualStrings(got, before) {
		t.Fatalf("ANSI sequences changed:\n got %q\nwant %q", got, before)
	}

	jsonIn := []byte(`{"command":"curl -H 'Authorization:Basic dXNlcjpwYXNz' https://x","ok":true}`)
	jsonOut := RedactJSON(jsonIn)
	if strings.Contains(jsonOut, "dXNlcjpwYXNz") {
		t.Fatalf("credential leaked from JSON: %q", jsonOut)
	}
	if !strings.Contains(jsonOut, `"ok":true`) {
		t.Fatalf("JSON lost non-secret content: %q", jsonOut)
	}
}
