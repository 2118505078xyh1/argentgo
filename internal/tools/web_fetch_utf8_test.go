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

// TestWebFetchTruncationKeepsUTF8RuneIntact 守护 P0 输出截断
// 不变量：web_fetch 自身的 200 KB 上限绝不能给模型留下一个
// 被切一半的多字节 rune。放置一个三字节的汉字，使其字节
// 跨越 maxFetchBytes；过去朴素的字节切片只保留它的第一个
// 字节，而 clip() 的 rune 安全头尾窗口随后把这个坏字节
// 直接带进工具结果。
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
	// 宽松的输出上限使 clip() 不会掩盖被测上限：无论哪种情况，
	// web_fetch 自身截断文本中都不应出现损坏的 rune。
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

// TestTruncateFetchBodyRuneBoundary 直接测试该辅助函数，使上限
// 在 ASCII 和多字节边界都能被检查，且与 HTTP 无关。
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
