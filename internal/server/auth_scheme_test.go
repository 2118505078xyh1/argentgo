package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPromptBearerSchemeIsCaseInsensitive 守护 HTTP 认证边界：根据
// RFC 7235，认证方案不区分大小写，因此发送
// "Authorization: bearer TOKEN"（或任意混合大小写）的客户端必须
// 与规范的 "Bearer TOKEN" 一样通过认证。区分大小写的字符串前缀
// 会以虚假的 401 拒绝这些客户端。
// TestPromptBearerTokenStaysCaseSensitive 确保大小写折叠只适用于
// 方案，绝不适用于凭据：当配置的 token 大小写不同时，"bearer SECRET" 仍会失败，
// 非 bearer 方案也会被拒绝。
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
