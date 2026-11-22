package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchRejectsContentType 覆盖 Content-Type 预检的判定边界：
// 只拒绝无歧义的二进制类型，缺失/畸形/模糊类型保持放行。
func TestFetchRejectsContentType(t *testing.T) {
	cases := []struct {
		kind string
		want bool
	}{
		{"", false},
		{"garbage;;;", false},
		{"text/html; charset=utf-8", false},
		{"application/json", false},
		{"application/xml", false},
		{"application/x-www-form-urlencoded", false},
		{"application/octet-stream", true},
		{"image/png", true},
		{"IMAGE/PNG", true},
		{"audio/mpeg", true},
		{"video/mp4", true},
		{"application/pdf", true},
		{"application/zip", true},
		{"application/gzip", true},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if got := fetchRejectsContentType(tc.kind); got != tc.want {
				t.Fatalf("fetchRejectsContentType(%q) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

// TestWebFetchSkipsBinaryBody 验证接线：服务端返回 image/png 时，
// 即使正文是可读文本，工具也不下载正文而是直接报二进制内容错误。
func TestWebFetchSkipsBinaryBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("this body must not be fetched for an image response"))
	}))
	defer srv.Close()

	args, err := json.Marshal(map[string]any{"url": srv.URL, "allowLocal": true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (webFetchTool{}).Run(context.Background(), "", args)
	if err == nil {
		t.Fatal("web_fetch on image/png response succeeded, want a binary-content error")
	}
	if !strings.Contains(err.Error(), "binary content") {
		t.Fatalf("err = %q, want it to mention binary content", err)
	}
}
