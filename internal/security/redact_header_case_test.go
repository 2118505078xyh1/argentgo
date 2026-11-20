package security

import (
	"bytes"
	"strings"
	"testing"
)

// TestRedactHeaderNamesAreCaseInsensitive covers RFC 7230/7235 case-insensitive
// header and auth-scheme names. headerPrefixes used to hold exact-case needles
// and searched the raw text, so `AUTHORIZATION: Basic <b64>` — a perfectly
// valid header — leaked the whole base64 credential: a bare base64 token is
// not credential-shaped on its own and no other rule matched it. The fix
// matches the needles against the ASCII-lowercased copy (byte offsets stay
// valid), which also makes a standalone `BEARER <token>` safe.
func TestRedactHeaderNamesAreCaseInsensitive(t *testing.T) {
	const secret = "dXNlcjpwYXNz"
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "uppercase header with OWS",
			in:      "AUTHORIZATION: Basic " + secret,
			absent:  []string{"Basic", secret},
			present: []string{"AUTHORIZATION:", placeholder("auth header")},
		},
		{
			name:    "mixed case header with OWS",
			in:      "AuThOrIzAtIoN: Bearer abc.def.ghi",
			absent:  []string{"Bearer", "abc.def.ghi"},
			present: []string{"AuThOrIzAtIoN:", placeholder("auth header")},
		},
		{
			name:    "uppercase header without OWS",
			in:      "AUTHORIZATION:Basic " + secret,
			absent:  []string{"Basic", secret},
			present: []string{"AUTHORIZATION:", placeholder("auth header")},
		},
		{
			name:    "lowercase header without OWS",
			in:      "authorization:Mac token=abc123",
			absent:  []string{"Mac", "abc123"},
			present: []string{placeholder("auth header")},
		},
		{
			name:    "inside a shell header argument",
			in:      `curl -H 'AUTHORIZATION: Basic ` + secret + `' https://example.com`,
			absent:  []string{"Basic", secret},
			present: []string{"curl -H", "https://example.com", placeholder("auth header")},
		},
		{
			name:    "standalone uppercase bearer scheme",
			in:      "retry with BEARER abc.def.ghi now",
			absent:  []string{"abc.def.ghi"},
			present: []string{"BEARER", placeholder("bearer token")},
		},
		{
			name:    "standalone mixed case bearer scheme",
			in:      "retry with BeArEr abc.def.ghi now",
			absent:  []string{"abc.def.ghi"},
			present: []string{"BeArEr", placeholder("bearer token")},
		},
		{
			name:    "every header on one line is scrubbed",
			in:      "AUTHORIZATION: Basic " + secret + "; Authorization: Bearer abc.def.ghi",
			absent:  []string{secret, "abc.def.ghi"},
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

// TestRedactHeaderCaseStructuredPaths checks that the case-insensitive header
// rule also protects the terminal row and audit JSON paths, and that ANSI
// colour sequences keep their exact positions relative to the surrounding text.
func TestRedactHeaderCaseStructuredPaths(t *testing.T) {
	const secret = "dXNlcjpwYXNz"

	line := []byte("\x1b[32mOK\x1b[0m AUTHORIZATION: Basic " + secret + " \x1b[1mend\x1b[0m")
	before := ansiSequences(line)
	out := RedactRow(line)
	if bytes.Contains(out, []byte(secret)) {
		t.Fatalf("credential leaked into row: %q", out)
	}
	if got := ansiSequences(out); !reflectDeepEqualStrings(got, before) {
		t.Fatalf("ANSI sequences changed:\n got %q\nwant %q", got, before)
	}

	jsonIn := []byte(`{"command":"curl -H 'AUTHORIZATION: Basic ` + secret + `' https://x","ok":true}`)
	jsonOut := RedactJSON(jsonIn)
	if strings.Contains(jsonOut, secret) {
		t.Fatalf("credential leaked from JSON: %q", jsonOut)
	}
	if !strings.Contains(jsonOut, `"ok":true`) {
		t.Fatalf("JSON lost non-secret content: %q", jsonOut)
	}
}
