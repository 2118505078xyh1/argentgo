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

// TestWebSearchTruncationKeepsUTF8RuneIntact 守护 web_search 的
// 输出截断不变量：其自身 200 KB 上限绝不能把多字节
// rune 切成两半。web_fetch 曾有完全相同的 bug（已用 truncateFetchBody 修复）；
// web_search 仍使用原始的 body[:maxFetchBytes] 切片，因此跨越上限的
// 汉字/emoji rune 会留下无效的尾部碎片，被 json.Unmarshal
// 拒绝 —— 把有效搜索响应变成工具错误。
//
// 响应是一个完整 JSON 对象，后跟一个 CJK 字符，其
// 前两个字节位于保留窗口的最后两个位置。
// 旧的字节切片恰好保留这两个字节并破坏解析；rune 安全的
// 上限丢弃整个字符，JSON 得以解析。
func TestWebSearchTruncationKeepsUTF8RuneIntact(t *testing.T) {
	const marker = "中" // 3-byte Han rune
	jsonPart := `{"results":[{"title":"Go","url":"https://go.dev","snippet":"The Go language"}]}`
	// 放置该 rune，使其前两个字节成为
	// maxFetchBytes 窗口的最后两个字节：第三个字节仍会被 LimitReader
	// 读取（它读取 maxFetchBytes+1）但被上限丢弃。
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
