package llm

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestContentFilterGuidanceProviders 覆盖各类真实世界中表示内容审核拦截的文案。
// 无论措辞如何，返回值都必须以可操作的默认引导开头，并在有细节时附上摘录。
func TestContentFilterGuidanceProviders(t *testing.T) {
	messages := []string{
		"provider returned 400 Bad Request: content_policy_violation",
		"content filter triggered",
		"blocked by safety filter",
		"safety check failed",
		"flagged by moderation",
		"policy violation: prohibited content",
		"内容审核未通过",
		"内容被安全策略拦截",
		"触发内容审核，请修改",
	}
	for _, msg := range messages {
		t.Run(msg, func(t *testing.T) {
			got := contentFilterGuidance(msg)
			if !strings.HasPrefix(got, defaultContentFilterGuidance) {
				t.Errorf("contentFilterGuidance(%q) = %q, want prefix %q", msg, got, defaultContentFilterGuidance)
			}
			if !strings.Contains(got, "提供方提示") {
				t.Errorf("contentFilterGuidance(%q) = %q, want a provider detail excerpt", msg, got)
			}
			if !utf8.ValidString(got) {
				t.Errorf("contentFilterGuidance(%q) = %q, invalid UTF-8", msg, got)
			}
		})
	}
}

// TestContentFilterGuidanceEmpty 确认空串与仅含空白的输入都返回默认引导，
// 且不带任何"提供方提示"括号。
func TestContentFilterGuidanceEmpty(t *testing.T) {
	for _, msg := range []string{"", "   ", "\t\n  \r\n"} {
		t.Run("empty/"+strings.TrimSpace(msg), func(t *testing.T) {
			if got := contentFilterGuidance(msg); got != defaultContentFilterGuidance {
				t.Errorf("contentFilterGuidance(%q) = %q, want %q", msg, got, defaultContentFilterGuidance)
			}
		})
	}
}

// TestContentFilterGuidanceDefaultGuidanceIsActionable 锁定默认引导本身的内容：
// 它必须同时包含"调整措辞""重试"与"审核"，确保是一条可执行的指令。
func TestContentFilterGuidanceDefaultGuidanceIsActionable(t *testing.T) {
	for _, phrase := range []string{"调整措辞", "重试", "审核"} {
		if !strings.Contains(defaultContentFilterGuidance, phrase) {
			t.Errorf("default guidance %q must contain %q", defaultContentFilterGuidance, phrase)
		}
	}
}

// TestContentFilterGuidanceCJKAndBounds 确认 CJK 消息被有界截断且绝不切断 UTF-8 字符：
// 摘录长度不超过上限，结尾以省略号标示截断，且整体仍是合法 UTF-8。
func TestContentFilterGuidanceCJKAndBounds(t *testing.T) {
	long := strings.Repeat("敏感词", 200) // 600 个 rune，远超上限
	got := contentFilterGuidance(long)
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	detail := contentFilterDetail(long)
	if runes := []rune(detail); len(runes) > contentFilterGuidanceMaxDetail {
		t.Errorf("detail has %d runes, want <= %d", len(runes), contentFilterGuidanceMaxDetail)
	}
	if !strings.HasSuffix(detail, "…") {
		t.Errorf("truncated detail %q should end with ellipsis", detail)
	}
	if !strings.HasPrefix(got, defaultContentFilterGuidance) {
		t.Errorf("result %q lost the actionable prefix", got)
	}
}

// TestContentFilterGuidanceLongASCIIBounded 覆盖超长 ASCII 文案的有界性。
// 填充字符用 'z' 而非 'a'，避免整段被 hexBlobRE 当成密钥脱敏，从而真正走到截断分支。
func TestContentFilterGuidanceLongASCIIBounded(t *testing.T) {
	msg := "content_policy_violation: " + strings.Repeat("z", 5000)
	got := contentFilterGuidance(msg)
	detail := contentFilterDetail(msg)
	if runes := []rune(detail); len(runes) > contentFilterGuidanceMaxDetail {
		t.Errorf("detail has %d runes, want <= %d", len(runes), contentFilterGuidanceMaxDetail)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(detail, "…") {
		t.Errorf("unexpected bounded result: detail=%q", detail)
	}
}

// TestContentFilterGuidanceRedactsCredentials 确认原始错误中的凭据、邮箱等
// 在写入引导文字前被脱敏，绝不会泄漏到历史记录。
func TestContentFilterGuidanceRedactsCredentials(t *testing.T) {
	msg := "content filter: api_key=sk-abcdefghijklmnop user@example.com password=hunter2secret99"
	got := contentFilterGuidance(msg)
	for _, secret := range []string{"sk-abcdefghijklmnop", "user@example.com", "hunter2secret99"} {
		if strings.Contains(got, secret) {
			t.Errorf("guidance leaked %q: %q", secret, got)
		}
	}
	if !strings.Contains(got, "[redacted]") || !strings.Contains(got, "[email]") {
		t.Errorf("expected redaction placeholders in %q", got)
	}
}

// TestContentFilterGuidanceSingleLine 确认摘录中的换行/制表被归一为空格，
// 使引导文字可作为单行历史记录安全追加。
func TestContentFilterGuidanceSingleLine(t *testing.T) {
	got := contentFilterGuidance("内容审核\n未通过\tpolicy violation")
	if strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("guidance should be single-line, got %q", got)
	}
}

// TestContentFilterGuidanceDeterministic 确认函数是纯的：相同输入重复调用结果一致。
func TestContentFilterGuidanceDeterministic(t *testing.T) {
	msg := "content_policy_violation: 请调整"
	first := contentFilterGuidance(msg)
	for i := 0; i < 20; i++ {
		if got := contentFilterGuidance(msg); got != first {
			t.Fatalf("call %d = %q, want %q", i, got, first)
		}
	}
}
