package server

import (
	"strings"
	"testing"
)

// TestRequestIDFromHeaderValid 覆盖合法关联 ID：普通值、前后空白被修剪、
// 常见 ID 形态（UUID / 十六进制 / 带分隔符）原样保留。
func TestRequestIDFromHeaderValid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{"简单 ID", "abc-123", "abc-123"},
		{"前后空白被修剪", "  req-42\t", "req-42"},
		{"换行空白被修剪", "\n\treq-7\r\n", "req-7"},
		{"UUID", "550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
		{"十六进制", "9f8e7d6c5b4a3210", "9f8e7d6c5b4a3210"},
		{"带点号与冒号", "svc-a:trace.01", "svc-a:trace.01"},
		{"内部空白保留", "a b", "a b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestIDFromHeader(tc.header); got != tc.want {
				t.Fatalf("requestIDFromHeader(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// TestRequestIDFromHeaderEmpty 覆盖空值、仅空白与仅控制字符：均返回空串。
func TestRequestIDFromHeaderEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"空串", ""},
		{"空格", "   "},
		{"制表符与换行", " \t\n\r "},
		{"仅 NUL 控制字符", "\x00\x00"},
		{"仅 DEL 控制字符", "\x7f"},
		{"仅 C1 控制字符", "\u0085\u009f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestIDFromHeader(tc.header); got != "" {
				t.Fatalf("requestIDFromHeader(%q) = %q, want 空串", tc.header, got)
			}
		})
	}
}

// TestRequestIDFromHeaderFiltersControl 覆盖控制字符过滤：首尾与中间的控制
// 字符都被移除，剩余可见字符被拼回。
func TestRequestIDFromHeaderFiltersControl(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{"首尾 NUL", "\x00abc\x00", "abc"},
		{"中间换行", "ab\ncd", "abcd"},
		{"混合控制字符", "a\x00b\tc\rd\x7fe", "abcde"},
		{"C1 控制字符", "a\u0085b\u009fc", "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestIDFromHeader(tc.header); got != tc.want {
				t.Fatalf("requestIDFromHeader(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// TestRequestIDFromHeaderCJK 验证 CJK 等 Unicode 多字节字符被按码点处理：
// 原样保留、不会被拆成半个 UTF-8 序列，截断也发生在码点边界。
func TestRequestIDFromHeaderCJK(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   string
	}{
		{"中文 ID", "请求-42", "请求-42"},
		{"日文假名", "リクエスト7", "リクエスト7"},
		{" emoji 与 CJK 混合", "req-\U0001F680-请求", "req-\U0001F680-请求"},
		{"CJK 首尾空白", "  请求42\t", "请求42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestIDFromHeader(tc.header); got != tc.want {
				t.Fatalf("requestIDFromHeader(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// TestRequestIDFromHeaderInvalidUTF8 验证包含非法 UTF-8 字节序列的头部
// 被视为非法，整串返回空串；同时保证不会在字节中间截断出畸形片段。
func TestRequestIDFromHeaderInvalidUTF8(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
	}{
		{"单独非法字节", "abc\xffdef"},
		{"截断的多字节序列", "请求\xe4\xb8"},
		{"仅有非法字节", "\xff\xfe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestIDFromHeader(tc.header); got != "" {
				t.Fatalf("requestIDFromHeader(%q) = %q, want 空串", tc.header, got)
			}
		})
	}
}

// TestRequestIDFromHeaderMaxLength 验证长度上限按码点计：恰好等于上限时
// 原样保留；超过上限时截断到上限，且 CJK 不会从中间被切断。
func TestRequestIDFromHeaderMaxLength(t *testing.T) {
	ascii := strings.Repeat("a", maxRequestIDRunes)
	if got := requestIDFromHeader(ascii); got != ascii {
		t.Fatalf("恰好 %d 个码点应原样保留，得到长度 %d", maxRequestIDRunes, len([]rune(got)))
	}

	over := strings.Repeat("b", maxRequestIDRunes+10)
	got := requestIDFromHeader(over)
	if want := strings.Repeat("b", maxRequestIDRunes); got != want {
		t.Fatalf("超过上限应截断到 %d 个码点，得到长度 %d", maxRequestIDRunes, len([]rune(got)))
	}

	// 每个「请」是三字节；用 CJK 验证截断发生在码点边界，结果仍是合法 UTF-8。
	cjk := strings.Repeat("请", maxRequestIDRunes+1)
	gotCJK := requestIDFromHeader(cjk)
	if want := strings.Repeat("请", maxRequestIDRunes); gotCJK != want {
		t.Fatalf("CJK 截断应保留 %d 个完整码点，得到 %d 个", maxRequestIDRunes, len([]rune(gotCJK)))
	}
}

// TestRequestIDFromHeaderControlDoesNotConsumeQuota 验证控制字符不占用长度
// 配额：先过滤控制字符再计长度，因此含控制字符的超长串仍能截取出完整 ID。
func TestRequestIDFromHeaderControlDoesNotConsumeQuota(t *testing.T) {
	// 在 128 个有效字符之间插入控制字符，过滤后应恰好保留 128 个有效字符。
	var b strings.Builder
	for i := 0; i < maxRequestIDRunes; i++ {
		b.WriteByte('\n')
		b.WriteByte('x')
	}
	want := strings.Repeat("x", maxRequestIDRunes)
	if got := requestIDFromHeader(b.String()); got != want {
		t.Fatalf("控制字符不应占用长度配额，得到长度 %d，want %d", len([]rune(got)), maxRequestIDRunes)
	}
}
