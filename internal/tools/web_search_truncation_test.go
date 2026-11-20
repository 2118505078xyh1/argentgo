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

// TestWebSearchTruncationKeepsUTF8RuneIntact guards the output-truncation
// invariant for web_search: its own 200 KB cap must never cut a multibyte
// rune in half. web_fetch had the identical bug (fixed with truncateFetchBody);
// web_search still used a raw body[:maxFetchBytes] slice, so a Han/emoji rune
// straddling the cap left an invalid trailing fragment that json.Unmarshal
// rejected -- turning a valid search response into a tool error.
//
// The response is a complete JSON object followed by a trailing CJK character
// whose first two bytes sit at the last two positions of the kept window. The
// old byte slice kept exactly those two bytes and broke parsing; the rune-safe
// cap drops the whole character and the JSON parses.
func TestWebSearchTruncationKeepsUTF8RuneIntact(t *testing.T) {
	const marker = "中" // 3-byte Han rune
	jsonPart := `{"results":[{"title":"Go","url":"https://go.dev","snippet":"The Go language"}]}`
	// Place the rune so its first two bytes are the last two bytes of the
	// maxFetchBytes window: the third byte is still read by LimitReader
	// (which reads maxFetchBytes+1) but dropped by the cap.
	padding := strings.Repeat(" ", maxFetchBytes-2-len(jsonPart))
	body := jsonPart + padding + marker + strings.Repeat(" ", 64)
	if len(body) <= maxFetchBytes {
		t.Fatalf("test body must exceed the cap: got %d bytes", len(body))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	t.Setenv(webSearchURLEnv, server.URL)

	args, err := json.Marshal(map[string]any{"query": "golang"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := (webSearchTool{}).Run(context.Background(), t.TempDir(), args)
	if err != nil {
		t.Fatalf("web_search must not fail on a rune-straddling cap boundary: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatal("web_search result is not valid UTF-8")
	}
	for _, want := range []string{"Go", "https://go.dev", "The Go language"} {
		if !strings.Contains(out, want) {
			t.Fatalf("web_search result %q is missing %q", out, want)
		}
	}
}
