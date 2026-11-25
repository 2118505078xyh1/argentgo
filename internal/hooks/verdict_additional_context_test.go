package hooks

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/security"
)

// TestAdditionalContextTopLevel 校验顶层 additionalContext 的提取。
func TestAdditionalContextTopLevel(t *testing.T) {
	stdout := `{"permissionDecision":"allow","additionalContext":"记住用户偏好： coffee 无糖"}`
	got := AdditionalContextFromOutcome(Outcome{Stdout: stdout})
	if got != "记住用户偏好： coffee 无糖" {
		t.Fatalf("top-level additionalContext = %q", got)
	}
}

// TestAdditionalContextHookSpecificOutput 校验 hookSpecificOutput.additionalContext 形态。
func TestAdditionalContextHookSpecificOutput(t *testing.T) {
	stdout := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","additionalContext":"nested ctx"}}`
	got := AdditionalContextFromOutcome(Outcome{Stdout: stdout})
	if got != "nested ctx" {
		t.Fatalf("nested additionalContext = %q", got)
	}
}

// TestAdditionalContextPriority 校验两种形态的优先级：同一对象内顶层优先；
// 顶层值非法（缺失或类型不符）时才回退到 hookSpecificOutput。
func TestAdditionalContextPriority(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			"top_wins",
			`{"additionalContext":"top","hookSpecificOutput":{"additionalContext":"nested"}}`,
			"top",
		},
		{
			"top_invalid_type_falls_back_to_nested",
			`{"additionalContext":123,"hookSpecificOutput":{"additionalContext":"nested"}}`,
			"nested",
		},
		{
			"top_empty_falls_back_to_nested",
			`{"additionalContext":"","hookSpecificOutput":{"additionalContext":"nested"}}`,
			"nested",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AdditionalContextFromOutcome(Outcome{Stdout: tt.stdout}); got != tt.want {
				t.Fatalf("AdditionalContextFromOutcome = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAdditionalContextByteLimitCJKSafe 校验字节上限截断：结果不超过
// maxAdditionalContextBytes 字节，在 rune 边界截断并附加省略号，CJK 不被切开。
func TestAdditionalContextByteLimitCJKSafe(t *testing.T) {
	context := strings.Repeat("上下文内容", 20000) // 远超 8192 字节的合法 CJK 文本
	stdout := `{"additionalContext":` + strconv.Quote(context) + `}`
	got := AdditionalContextFromOutcome(Outcome{Stdout: stdout})

	if got == "" {
		t.Fatal("expected non-empty truncated context")
	}
	if len(got) > maxAdditionalContextBytes {
		t.Fatalf("context not bounded: %d > %d", len(got), maxAdditionalContextBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncated context must remain valid UTF-8")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated context must end with ellipsis, got tail %q", got[len(got)-min(6, len(got)):])
	}
	// 省略号之前的正文必须是原文前缀，证明是尾部截断而非中间篡改。
	if body := strings.TrimSuffix(got, "…"); !strings.HasPrefix(context, body) {
		t.Fatal("truncated body must be a prefix of the original context")
	}
	// 与 clipRunes 的显式约定保持一致。
	if want := clipRunes(context, additionalContextClipLimit); got != want {
		t.Fatalf("truncation mismatch: got len %d, want len %d", len(got), len(want))
	}

	// 刚好在上限内的短 CJK 文本应原样保留，不附加省略号。
	short := "这是一段很短的中文附加上下文"
	if got := AdditionalContextFromOutcome(Outcome{Stdout: `{"additionalContext":` + strconv.Quote(short) + `}`}); got != short {
		t.Fatalf("short CJK context must be preserved verbatim: %q", got)
	}
}

// TestAdditionalContextRedaction 校验凭据脱敏：注入文本先经 security.Redact，
// 不得原样泄漏密钥。
func TestAdditionalContextRedaction(t *testing.T) {
	secret := "sk-proj-abcdefghijklmnopqrstuvwxyz"
	text := "密钥是 " + secret + " 请保密"
	stdout := `{"additionalContext":` + strconv.Quote(text) + `}`
	got := AdditionalContextFromOutcome(Outcome{Stdout: stdout})

	if strings.Contains(got, secret) {
		t.Fatalf("context leaked credential: %q", got)
	}
	if want := strings.TrimSpace(security.Redact(text)); got != want {
		t.Fatalf("context = %q, want redacted %q", got, want)
	}
}

// TestAdditionalContextInvalidAndMissing 校验非法 / 缺失 / 类型不符一律返回空串。
func TestAdditionalContextInvalidAndMissing(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{"empty", ""},
		{"not_json", "no decision here"},
		{"malformed", `{"additionalContext":`},
		{"wrong_type_number", `{"additionalContext":123}`},
		{"wrong_type_object", `{"additionalContext":{}}`},
		{"null", `{"additionalContext":null}`},
		{"whitespace_only", `{"additionalContext":"   "}`},
		{"field_missing", `{"permissionDecision":"allow"}`},
		{"nested_wrong_type", `{"hookSpecificOutput":{"additionalContext":42}}`},
		{"nested_container_wrong_type", `{"hookSpecificOutput":123}`},
		{"log_brace_not_json", "config {not json}\n{\"permissionDecision\":\"deny\"}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AdditionalContextFromOutcome(Outcome{Stdout: tt.stdout}); got != "" {
				t.Fatalf("expected empty context, got %q", got)
			}
		})
	}
}

// TestAdditionalContextIgnoresOtherFields 校验明确不解析 systemMessage /
// suppressOutput 等其它字段（顶层与嵌套均不解析）。
func TestAdditionalContextIgnoresOtherFields(t *testing.T) {
	stdouts := []string{
		`{"systemMessage":"这是一条系统消息","suppressOutput":true}`,
		`{"hookSpecificOutput":{"systemMessage":"nested system message"}}`,
	}
	for _, stdout := range stdouts {
		if got := AdditionalContextFromOutcome(Outcome{Stdout: stdout}); got != "" {
			t.Fatalf("must not parse systemMessage/suppressOutput, got %q from %s", got, stdout)
		}
	}
}

// TestAdditionalContextTimeout 校验超时结果的 stdout 不可信，附加上下文为空。
func TestAdditionalContextTimeout(t *testing.T) {
	out := Outcome{TimedOut: true, Stdout: `{"additionalContext":"不该被注入"}`}
	if got := AdditionalContextFromOutcome(out); got != "" {
		t.Fatalf("timed-out context must be empty, got %q", got)
	}
}

// TestAdditionalContextLogPrefixAndMultipleObjects 校验日志前缀与多对象：
// 跳过非 JSON 前缀，跨对象时取第一个含合法 additionalContext 的对象。
func TestAdditionalContextLogPrefixAndMultipleObjects(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			"leading_log",
			"2026/01/01 hook starting\n{\"additionalContext\":\"first\"}",
			"first",
		},
		{
			"first_object_has_no_context",
			`{"permissionDecision":"deny"}{"additionalContext":"ctx"}`,
			"ctx",
		},
		{
			"first_nonempty_wins",
			`{"additionalContext":"first"}{"additionalContext":"second"}`,
			"first",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AdditionalContextFromOutcome(Outcome{Stdout: tt.stdout}); got != tt.want {
				t.Fatalf("AdditionalContextFromOutcome = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAdditionalContextDoesNotChangeVerdict 校验向后兼容：附加上下文的解析不影响
// 权限裁决；无 additionalContext 时提取结果为空串，裁决与现状一致。
func TestAdditionalContextDoesNotChangeVerdict(t *testing.T) {
	tests := []struct {
		name string
		out  Outcome
		want Verdict
		ctx  string
	}{
		{"allow_with_context", Outcome{Stdout: `{"permissionDecision":"allow","additionalContext":"ctx"}`}, VerdictAllow, "ctx"},
		{"deny_with_context", Outcome{Stdout: `{"permissionDecision":"deny","additionalContext":"ctx"}`}, VerdictDeny, "ctx"},
		{"allow_without_context", Outcome{ExitCode: 49, Stdout: `{"permissionDecision":"allow"}`}, VerdictAllow, ""},
		{"deny_without_context", Outcome{ExitCode: 2, Stdout: `{"permissionDecision":"deny"}`}, VerdictDeny, ""},
		{"no_signal", Outcome{ExitCode: 0}, VerdictNone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerdictFromOutcome(tt.out); got != tt.want {
				t.Fatalf("verdict = %d, want %d", got, tt.want)
			}
			if got := AdditionalContextFromOutcome(tt.out); got != tt.ctx {
				t.Fatalf("context = %q, want %q", got, tt.ctx)
			}
		})
	}
}
