package cli

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/auth"
)

// TestAuthExpiryAdviceByState 覆盖四种分类状态，确认只有需要行动的状态
// 才产生提示，valid 与未知取值保持静默。
func TestAuthExpiryAdviceByState(t *testing.T) {
	cases := []struct {
		name   string
		status auth.TokenState
		want   string // 期望包含的关键片段；空表示期望返回空串
	}{
		{"expired", auth.TokenExpired, "凭证已过期"},
		{"expiring", auth.TokenExpiring, "凭证即将过期"},
		{"unknown", auth.TokenUnknown, "凭证状态未知"},
		{"valid stays silent", auth.TokenValid, ""},
		{"unrecognised state stays silent", auth.TokenState("future_state"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := authExpiryAdvice("openai", tc.status)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("authExpiryAdvice(openai, %q) = %q, want empty", tc.status, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("authExpiryAdvice(openai, %q) = %q, want to contain %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestAuthExpiryAdviceIncludesProvider 确认提供方名称被写入 re-login 命令，
// 用户无需再猜测要为哪个账号刷新凭证。
func TestAuthExpiryAdviceIncludesProvider(t *testing.T) {
	for _, status := range []auth.TokenState{auth.TokenExpired, auth.TokenExpiring, auth.TokenUnknown} {
		got := authExpiryAdvice("anthropic", status)
		if !strings.Contains(got, "anthropic") {
			t.Fatalf("status %q: provider name missing from advice: %q", status, got)
		}
		if !strings.Contains(got, "argentgo login --provider anthropic") {
			t.Fatalf("status %q: re-login command missing from advice: %q", status, got)
		}
	}
}

// TestAuthExpiryAdviceCJKProviderIsIntact 确认 CJK 提供方名称被原样写入，
// 不出现 U+FFFD 替换字符（即没有在 UTF-8 字符中间被截断）。
func TestAuthExpiryAdviceCJKProviderIsIntact(t *testing.T) {
	const provider = "火山方舟·华东"
	for _, status := range []auth.TokenState{auth.TokenExpired, auth.TokenExpiring, auth.TokenUnknown} {
		got := authExpiryAdvice(provider, status)
		if !strings.Contains(got, provider) {
			t.Fatalf("status %q: CJK provider %q missing from advice: %q", status, provider, got)
		}
		if strings.ContainsRune(got, '\uFFFD') {
			t.Fatalf("status %q: advice contains replacement rune (broken UTF-8): %q", status, got)
		}
	}
}

// TestAuthExpiryAdviceEmptyProvider 确认空或纯空白的提供方名称不会生成
// 带空参数的无效命令（如 "--provider "）。
func TestAuthExpiryAdviceEmptyProvider(t *testing.T) {
	cases := []struct {
		status auth.TokenState
		want   string
	}{
		{auth.TokenExpired, "凭证已过期，请运行 argentgo login 重新登录"},
		{auth.TokenExpiring, "凭证即将过期，建议运行 argentgo login 提前刷新"},
		{auth.TokenUnknown, "请运行 argentgo auth 查看状态"},
	}
	for _, tc := range cases {
		got := authExpiryAdvice("   ", tc.status)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("status %q: advice %q does not contain %q", tc.status, got, tc.want)
		}
		if strings.Contains(got, "--provider") {
			t.Fatalf("status %q: empty provider produced invalid command: %q", tc.status, got)
		}
	}
}

// TestAuthExpiryAdviceTrimsProviderWhitespace 确认提供方名称两侧的空白
// 被 Unicode 安全地去掉，插入到命令中的是干净的名字。
func TestAuthExpiryAdviceTrimsProviderWhitespace(t *testing.T) {
	got := authExpiryAdvice("  openai  ", auth.TokenExpired)
	if !strings.Contains(got, "--provider openai") {
		t.Fatalf("provider whitespace not trimmed: %q", got)
	}
}
