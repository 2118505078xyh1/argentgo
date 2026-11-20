package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestWebFetchTruncationKeepsUTF8RuneIntact guards the P0 output-truncation
// invariant: web_fetch's own 200 KB cap must never leave the model with a
// half-cut multibyte rune. Place a three-byte Han character so its bytes
// straddle maxFetchBytes; the naive byte slice used to keep only its first
// byte, and clip()'s rune-safe head/tail windows then carried that broken byte
// straight into the tool result.
func TestWebFetchTruncationKeepsUTF8RuneIntact(t *testing.T) {
	body := strings.Repeat("a", maxFetchBytes-1) + "中" + strings.Repeat("b", 128)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	args, err := json.Marshal(map[string]any{"url": server.URL, "allowLocal": true})
	if err != nil {
		t.Fatal(err)
	}
	// A generous output limit keeps clip() from masking the cap under test: the
	// broken rune must be absent from web_fetch's own truncated text either way.
	ctx := context.WithValue(context.Background(), outputLimitKey{}, maxFetchBytes+1024)
	out, err := (webFetchTool{}).Run(ctx, t.TempDir(), args)
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	if !strings.Contains(out, "[truncated at 200 KB]") {
		t.Fatalf("response was not capped at %d bytes: %q", maxFetchBytes, out[:min(len(out), 80)])
	}
	if !utf8.ValidString(out) {
		t.Fatal("web_fetch truncated in the middle of a UTF-8 rune")
	}
}

// TestTruncateFetchBodyRuneBoundary exercises the helper directly so the cap
// is checked at both ASCII and multibyte boundaries, independent of HTTP.
func TestTruncateFetchBodyRuneBoundary(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		limit int
		want  string
	}{
		{"under limit", "abc", 10, "abc"},
		{"ascii aligned", "abcde", 3, "abc"},
		{"han straddle", "ab中cd", 3, "ab"},
		{"emoji straddle", "ab😀cd", 3, "ab"},
		{"rune ends at cap", "ab中cd", 5, "ab中"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit := tc.limit
			got := string(truncateFetchBody([]byte(tc.body), limit))
			if got != tc.want {
				t.Fatalf("truncateFetchBody(%q, %d) = %q, want %q", tc.body, limit, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result is not valid UTF-8: %q", got)
			}
		})
	}
}
