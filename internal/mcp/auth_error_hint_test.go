package mcp

import (
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestAuthErrorHintStatusMapping 验证只有 401/403 产生提示，其它状态码为空。
func TestAuthErrorHintStatusMapping(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"zero", 0, ""},
		{"negative", -1, ""},
		{"ok", 200, ""},
		{"bad request", 400, ""},
		{"not found", 404, ""},
		{"server error", 500, ""},
		{"bad gateway", 502, ""},
		{"unauthorized", 401, "needs-auth"},
		{"forbidden", 403, "needs-auth"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authErrorHint(tt.status, "some body")
			if tt.want == "" {
				if got != "" {
					t.Fatalf("authErrorHint(%d) = %q, 期望空串", tt.status, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("authErrorHint(%d) 为空，期望包含 %q", tt.status, tt.want)
			}
			if !strings.Contains(got, tt.want) {
				t.Fatalf("authErrorHint(%d) = %q，期望包含 %q", tt.status, got, tt.want)
			}
			if !strings.Contains(got, "认证失败") {
				t.Fatalf("authErrorHint(%d) = %q，期望包含中文认证提示", tt.status, got)
			}
		})
	}
}

// TestAuthErrorHintDifferentiates401And403 验证两种状态码给出不同的中文指引。
func TestAuthErrorHintDifferentiates401And403(t *testing.T) {
	hint401 := authErrorHint(401, "")
	hint403 := authErrorHint(403, "")
	if hint401 == hint403 {
		t.Fatalf("401 与 403 的提示不应相同：%q", hint401)
	}
	if !strings.Contains(hint401, "401") || !strings.Contains(hint401, "凭据") {
		t.Fatalf("401 提示缺少状态码或凭据指引：%q", hint401)
	}
	if !strings.Contains(hint403, "403") || !strings.Contains(hint403, "权限") {
		t.Fatalf("403 提示缺少状态码或权限指引：%q", hint403)
	}
}

// TestAuthErrorHintNoURLNoBodyEcho 验证无 URL 时只返回基础提示，绝不回显 body。
func TestAuthErrorHintNoURLNoBodyEcho(t *testing.T) {
	body := "Unauthorized: bad token SECRET_XYZ_12345, 禁止访问"
	got := authErrorHint(401, body)
	if strings.Contains(got, "SECRET_XYZ") {
		t.Fatalf("提示不应回显 body 中的敏感文本：%q", got)
	}
	if strings.Contains(got, "参考地址") {
		t.Fatalf("无 URL 时不应附带参考地址：%q", got)
	}
}

// TestAuthErrorHintExtractsAndRedactsURL 验证 URL 提取与脱敏的各种情形。
func TestAuthErrorHintExtractsAndRedactsURL(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "basic url with safe query",
			body:        `please see https://example.com/login?next=%2Fx`,
			wantContain: []string{"参考地址", "https://example.com/login", "next=%2Fx"},
		},
		{
			name:        "userinfo removed",
			body:        `auth at https://user:pass@example.com/x failed`,
			wantContain: []string{"https://example.com/x"},
			wantAbsent:  []string{"user:pass", "pass@"},
		},
		{
			name:        "sensitive query removed, safe kept",
			body:        `https://example.com/x?token=abc&api_key=xyz&keep=1`,
			wantContain: []string{"https://example.com/x", "keep=1"},
			wantAbsent:  []string{"token=abc", "api_key=xyz"},
		},
		{
			name:        "fragment removed",
			body:        `https://example.com/x#access_token=abc`,
			wantContain: []string{"https://example.com/x"},
			wantAbsent:  []string{"access_token", "#"},
		},
		{
			// CJK 主机与路径会被 url.String() 百分号编码，这里只确认
			// 提取成功；可读的 CJK 成分由下面的专项测试解析校验。
			name:        "cjk url extracted",
			body:        `请访问 https://例子.测试/授权 后重试`,
			wantContain: []string{"参考地址", "https://"},
		},
		{
			name:        "cjk sensitive query removed",
			body:        `https://例子.测试/授权?令牌=abc&保留=1`,
			wantContain: []string{"参考地址"},
			wantAbsent:  []string{"令牌"},
		},
		{
			name:        "earliest of http and https chosen",
			body:        `see http://first.test/x then https://second.test/y`,
			wantContain: []string{"http://first.test/x"},
			wantAbsent:  []string{"second.test"},
		},
		{
			name:        "trailing punctuation trimmed",
			body:        `visit https://example.com/x.`,
			wantContain: []string{"https://example.com/x"},
			wantAbsent:  []string{"https://example.com/x."},
		},
		{
			name:        "json quoted url",
			body:        `{"error":"https://example.com/auth?code=xyz"}`,
			wantContain: []string{"https://example.com/auth"},
			wantAbsent:  []string{"code=xyz", "\"}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authErrorHint(401, tt.body)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Fatalf("提示 %q 期望包含 %q", got, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Fatalf("提示 %q 不应包含 %q", got, absent)
				}
			}
		})
	}
}

// hintReferenceURL 从提示中取出参考地址并解析；不存在时返回 nil。
func hintReferenceURL(t *testing.T, hint string) *url.URL {
	t.Helper()
	const marker = "参考地址："
	i := strings.Index(hint, marker)
	if i < 0 {
		return nil
	}
	raw := strings.TrimSpace(hint[i+len(marker):])
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("参考地址无法解析：%q (%v)", raw, err)
	}
	return u
}

// TestAuthErrorHintCJKURLComponents 验证 CJK 成分被完整保留且敏感参数被移除。
// url.String() 会百分号编码非 ASCII 字符，因此这里解析 URL 后按组件断言。
func TestAuthErrorHintCJKURLComponents(t *testing.T) {
	hint := authErrorHint(401, `请访问 https://例子.测试/授权?令牌=abc&保留=1 后重试`)
	u := hintReferenceURL(t, hint)
	if u == nil {
		t.Fatalf("提示缺少参考地址：%q", hint)
	}
	if u.Host != "例子.测试" {
		t.Fatalf("CJK 主机被破坏：%q", u.Host)
	}
	if u.Path != "/授权" {
		t.Fatalf("CJK 路径被破坏：%q", u.Path)
	}
	query := u.Query()
	if got := query.Get("令牌"); got != "" {
		t.Fatalf("敏感参数 '令牌' 应被移除，实际为 %q", got)
	}
	if got := query.Get("保留"); got != "1" {
		t.Fatalf("非敏感参数 '保留' 应保留为 1，实际为 %q", got)
	}
	if !utf8.ValidString(hint) {
		t.Fatalf("提示不是合法 UTF-8：%q", hint)
	}
}

// TestAuthErrorHintMalformedURL 验证畸形 URL 不会产生参考地址。
func TestAuthErrorHintMalformedURL(t *testing.T) {
	for _, body := range []string{
		"http://",
		"https://",
		"http://%zz bad escape",
		"see http:// no host",
		"",
	} {
		got := authErrorHint(401, body)
		if strings.Contains(got, "参考地址") {
			t.Fatalf("畸形 body %q 不应产生参考地址：%q", body, got)
		}
	}
}

// TestAuthErrorHintBounded 验证超长 body 与超长 URL 都被限制，且不会切坏 CJK。
func TestAuthErrorHintBounded(t *testing.T) {
	// URL 落在扫描窗口之外，不应被提取。
	far := strings.Repeat("a", authHintMaxBodyScan+100) + " https://far.test/x"
	got := authErrorHint(401, far)
	if strings.Contains(got, "far.test") {
		t.Fatalf("超出扫描窗口的 URL 不应被提取：%q", got)
	}
	if utf8.RuneCountInString(got) > authHintMaxTotalLen {
		t.Fatalf("提示长度 %d 超过上限 %d", utf8.RuneCountInString(got), authHintMaxTotalLen)
	}

	// 超长 URL 被截断，最终产物仍合法且有界。
	longBody := "https://example.com/" + strings.Repeat("x", 2000)
	got = authErrorHint(403, longBody)
	if !utf8.ValidString(got) {
		t.Fatalf("截断后的提示不是合法 UTF-8：%q", got)
	}
	if utf8.RuneCountInString(got) > authHintMaxTotalLen {
		t.Fatalf("提示长度 %d 超过上限 %d", utf8.RuneCountInString(got), authHintMaxTotalLen)
	}

	// 超长 CJK URL 截断不切坏字符，也不产生替换符。
	cjkBody := "https://例子.测试/" + strings.Repeat("授", 500)
	got = authErrorHint(401, cjkBody)
	if !utf8.ValidString(got) {
		t.Fatalf("CJK 截断后的提示不是合法 UTF-8：%q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("CJK 截断不应产生替换符：%q", got)
	}
	if !strings.HasPrefix(got, "认证失败") || !strings.Contains(got, "https://") {
		t.Fatalf("CJK 提示应保留基础提示与 URL 前缀：%q", got)
	}
	if utf8.RuneCountInString(got) > authHintMaxTotalLen {
		t.Fatalf("提示长度 %d 超过上限 %d", utf8.RuneCountInString(got), authHintMaxTotalLen)
	}
}

// TestTruncateRunesRespectsRuneBoundaries 直接验证 rune 截断不切坏多字节字符。
func TestTruncateRunesRespectsRuneBoundaries(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"empty", "你好", 0, ""},
		{"exact", "你好世界", 2, "你好"},
		{"longer than input", "你好", 10, "你好"},
		{"ascii", "abcd", 2, "ab"},
		{"emoji", "a😀b", 2, "a😀"},
		{"negative max", "你好", -1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateRunes(tt.s, tt.max)
			if got != tt.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, 期望 %q", tt.s, tt.max, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("结果不是合法 UTF-8：%q", got)
			}
		})
	}
}

// TestAuthErrorHintDeterministic 验证纯函数特性：相同输入恒定返回相同输出。
func TestAuthErrorHintDeterministic(t *testing.T) {
	body := `see https://example.com/x?token=abc&keep=1`
	first := authErrorHint(401, body)
	for i := 0; i < 5; i++ {
		if got := authErrorHint(401, body); got != first {
			t.Fatalf("第 %d 次调用结果 %q 与首次 %q 不一致", i, got, first)
		}
	}
}
