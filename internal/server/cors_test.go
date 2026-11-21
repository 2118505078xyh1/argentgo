package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCorsAllowedWildcard 验证 allowed 含 "*" 时对任意非空来源放行，
// 包括普通来源、字符串 "null" 与 CJK 多字节来源。
func TestCorsAllowedWildcard(t *testing.T) {
	for _, origin := range []string{
		"https://example.com",
		"http://localhost:3000",
		"null",
		"https://例子.测试", // CJK 来源
	} {
		if !corsAllowed(origin, []string{"*"}) {
			t.Fatalf("corsAllowed(%q, [\"*\"]) = false, want true", origin)
		}
	}
	// 通配符与具体条目混排时依然放行。
	if !corsAllowed("https://other.example", []string{"https://example.com", "*"}) {
		t.Fatal("含 \"*\" 的混合列表应放行任意来源")
	}
}

// TestCorsAllowedExact 验证精确匹配与大小写不敏感匹配，同时确认不做
// 子串匹配、前缀匹配或 URL 解析。
func TestCorsAllowedExact(t *testing.T) {
	allowed := []string{"https://example.com", "http://localhost:3000"}
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"https://example.com", true},
		{"HTTPS://EXAMPLE.COM", true},   // 整体大小写不敏感
		{"HtTp://LOCALHOST:3000", true}, // 大小写不敏感
		{"https://example.org", false},
		{"https://example.com.evil", false}, // 不做后缀子串匹配
		{"https://example", false},          // 不做前缀匹配
		{"https://example.com/path", false}, // 不解析 URL 路径
	} {
		if got := corsAllowed(tc.origin, allowed); got != tc.want {
			t.Fatalf("corsAllowed(%q, %v) = %v, want %v", tc.origin, allowed, got, tc.want)
		}
	}
}

// TestCorsAllowedEmptyOrigin 验证空来源一律返回 false——即使列表含 "*"，
// 也要遵守「空 origin 返回 false」这条优先规则。
func TestCorsAllowedEmptyOrigin(t *testing.T) {
	for _, allowed := range [][]string{
		nil,
		{},
		{"*"},
		{"https://example.com"},
	} {
		if corsAllowed("", allowed) {
			t.Fatalf("corsAllowed(\"\", %v) = true, want false", allowed)
		}
	}
}

// TestCorsAllowedNoAllowed 验证允许列表为空时任何来源都不放行。
func TestCorsAllowedNoAllowed(t *testing.T) {
	for _, allowed := range [][]string{nil, {}} {
		if corsAllowed("https://example.com", allowed) {
			t.Fatalf("corsAllowed(..., %v) = true, want false", allowed)
		}
	}
}

// TestCorsAllowedCJK 验证 CJK 多字节来源按码点匹配：大小写折叠对中文无副作用，
// 精确命中；近似但不相等的来源不会被误判，也不会因字节截断而误命中。
func TestCorsAllowedCJK(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origin  string
		allowed []string
		want    bool
	}{
		{"中文来源命中", "https://例子.测试", []string{"https://例子.测试"}, true},
		{"中文来源大小写折叠后命中", "https://例子.测试", []string{"HTTPS://例子.测试"}, true},
		{"日文来源命中", "https://例え.テスト", []string{"https://例え.テスト"}, true},
		{"CJK 不匹配", "https://例子.测试", []string{"https://示例.测试"}, false},
		{"CJK 不因字节截断误命中", "https://例子.测试", []string{"https://例子.测"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := corsAllowed(tc.origin, tc.allowed); got != tc.want {
				t.Fatalf("corsAllowed(%q, %v) = %v, want %v", tc.origin, tc.allowed, got, tc.want)
			}
		})
	}
}

// TestCorsAllowedStability 验证纯函数确定性：重复调用结果一致，且不修改入参切片。
func TestCorsAllowedStability(t *testing.T) {
	allowed := []string{"https://example.com", "*"}
	orig := append([]string(nil), allowed...)
	for i := 0; i < 3; i++ {
		if got := corsAllowed("https://other.example", allowed); !got {
			t.Fatalf("第 %d 次调用 = false, want true", i)
		}
	}
	for i := range allowed {
		if allowed[i] != orig[i] {
			t.Fatalf("入参被修改：allowed[%d] = %q, want %q", i, allowed[i], orig[i])
		}
	}
}

// newCORSHandler 构造一个带 CORS 配置的测试处理器。
func newCORSHandler(allowed ...string) http.Handler {
	return NewHandler(Config{
		Token:          "s3cret",
		Runner:         &fakeRunner{reply: "ok"},
		AllowedOrigins: allowed,
	})
}

// preflightRequest 构造一个标准的浏览器预检请求。
func preflightRequest(origin, method, headers string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, "/v1/prompt", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", method)
	if headers != "" {
		req.Header.Set("Access-Control-Request-Headers", headers)
	}
	return req
}

// TestPreflightAllowed 验证允许的来源发起预检时返回 204，并补齐全部 CORS 头。
func TestPreflightAllowed(t *testing.T) {
	h := newCORSHandler("https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, preflightRequest("https://EXAMPLE.com", "POST", "authorization, content-type"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检状态 = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://EXAMPLE.com" {
		t.Fatalf("Allow-Origin = %q, want 回显来源", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Fatalf("Allow-Methods = %q, want %q", got, corsAllowMethods)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "authorization, content-type" {
		t.Fatalf("Allow-Headers = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != corsMaxAge {
		t.Fatalf("Max-Age = %q, want %q", got, corsMaxAge)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want 含 Origin", got)
	}
}

// TestPreflightAllowedWildcard 验证通配符列表放行预检，且回显具体来源而非 "*"。
func TestPreflightAllowedWildcard(t *testing.T) {
	h := newCORSHandler("*")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, preflightRequest("https://any.example", "POST", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检状态 = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://any.example" {
		t.Fatalf("Allow-Origin = %q, want 回显具体来源", got)
	}
}

// TestPreflightRejected 验证未允许的来源发起预检时被 403 拒绝，且不回显 Allow-Origin。
func TestPreflightRejected(t *testing.T) {
	h := newCORSHandler("https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, preflightRequest("https://evil.example", "POST", "authorization"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("预检状态 = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("拒绝时不应回显 Allow-Origin，得到 %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("拒绝时不应宣告 Allow-Methods，得到 %q", got)
	}
}

// TestPreflightNoConfiguredOrigins 验证未配置 AllowedOrigins 时预检被拒绝，
// 与原有的"未配置即不放行"约定一致。
func TestPreflightNoConfiguredOrigins(t *testing.T) {
	h := newCORSHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, preflightRequest("https://example.com", "POST", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("预检状态 = %d, want 403", rec.Code)
	}
}

// TestCORSHeadersOnActualRequest 验证真实（非预检）GET 请求也带上 CORS 响应头。
func TestCORSHeadersOnActualRequest(t *testing.T) {
	h := newCORSHandler("*")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Origin", "https://example.com")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want 含 Origin", got)
	}
}

// TestCORSNoHeadersWithoutOrigin 验证没有 Origin 头的请求不被改动，保持原有行为。
func TestCORSNoHeadersWithoutOrigin(t *testing.T) {
	h := newCORSHandler("*")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态 = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("无 Origin 时不应有 Allow-Origin，得到 %q", got)
	}
	if got := rec.Header().Get("Vary"); strings.Contains(got, "Origin") {
		t.Fatalf("无 Origin 时不应追加 Vary: Origin，得到 %q", got)
	}
}

// TestNonPreflightOptionsUnchanged 验证缺少 Access-Control-Request-Method 的
// OPTIONS 不被当作预检，仍交给路由按原有的 405 逻辑处理。
func TestNonPreflightOptionsUnchanged(t *testing.T) {
	h := newCORSHandler("*")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/prompt", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态 = %d, want 405", rec.Code)
	}
}
