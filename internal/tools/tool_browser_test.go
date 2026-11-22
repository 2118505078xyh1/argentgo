package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFastFetchTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>快抓测试</h1><p>静态页面内容</p></body></html>`)
	}))
	defer srv.Close()

	tool := fastFetchTool{}
	out, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL)))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"快抓测试", "静态页面内容", "200"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestFastFetchToolRequiresURL(t *testing.T) {
	tool := fastFetchTool{}
	if _, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for missing url")
	}
}

// TestBrowserToolWithCDP 用本机 CDP（9333）验证工具的 open→eval 闭环。
// 没有端点时跳过，不影响常规测试。
func TestBrowserToolWithCDP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><meta charset="utf-8"><title>工具测试页</title></head><body><button id="b">点我</button></body></html>`)
	}))
	defer srv.Close()

	tool := &browserTool{mgr: NewBrowserManager(func(string) string { return "http://127.0.0.1:9333" })}
	root := t.TempDir()
	out, err := tool.Run(context.Background(), root, json.RawMessage(fmt.Sprintf(`{"action":"open","url":%q,"level":"skeleton"}`, srv.URL)))
	if err != nil {
		t.Skipf("no CDP endpoint: %v", err)
	}
	if !strings.Contains(out, "工具测试页") || !strings.Contains(out, "@1") {
		t.Fatalf("open output missing title/ref: %q", out)
	}
	got, err := tool.Run(context.Background(), root, json.RawMessage(`{"action":"eval","text":"document.title"}`))
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got != "工具测试页" {
		t.Fatalf("eval title = %q", got)
	}
}

func TestRegisterBrowserIsIdempotent(t *testing.T) {
	r := New(t.TempDir(), false)
	before := len(r.order)
	RegisterBrowser(r)
	first := len(r.order)
	RegisterBrowser(r)
	if len(r.order) != first {
		t.Fatalf("RegisterBrowser not idempotent: %d -> %d -> %d", before, first, len(r.order))
	}
	if first != before+2 {
		t.Fatalf("expected +2 tools, got %d -> %d", before, first)
	}
	if r.tools[browserToolName] == nil || r.tools[fastFetchToolName] == nil {
		t.Fatal("browser/fastfetch tools not registered")
	}
}
