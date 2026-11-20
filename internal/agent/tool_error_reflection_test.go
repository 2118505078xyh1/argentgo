package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestToolErrorReflectionWrapsVariousErrors 检查各类工具错误都能被包装成
// "指令 → 错误头 → 正文" 的统一布局，标签被大写化，正文原样保留。
func TestToolErrorReflectionWrapsVariousErrors(t *testing.T) {
	cases := []struct {
		name  string
		label string
		err   string
		head  string
	}{
		{"畸形参数 JSON", "edit", "bad arguments: cannot parse JSON", "[tool: EDIT] ERROR"},
		{"无效参数", "bash", "invalid arguments: missing field path", "[tool: BASH] ERROR"},
		{"编辑格式", "multi_edit", "edit format: old text not found", "[tool: MULTI_EDIT] ERROR"},
		{"审批拒绝", "shell", "error: the operator denied this call", "[tool: SHELL] ERROR"},
		{"带空白的标签", "  edit  ", "something failed", "[tool: EDIT] ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolErrorReflection(tc.label, tc.err)
			prefix := toolErrorFixInstruction + "\n" + tc.head + "\n"
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("feedback = %q, want prefix %q", got, prefix)
			}
			if !strings.HasSuffix(got, strings.TrimSpace(tc.err)) {
				t.Fatalf("feedback = %q, want the error text kept as the tail", got)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("feedback is not valid UTF-8: %q", got)
			}
		})
	}
}

// TestToolErrorReflectionCJKKept 检查中文错误与中文标签在反射反馈中保持完整且合法。
func TestToolErrorReflectionCJKKept(t *testing.T) {
	const msg = "编辑失败：未找到匹配的锚点文本"
	got := toolErrorReflection("编辑", msg)
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	if !strings.Contains(got, "[tool: 编辑] ERROR") {
		t.Fatalf("feedback = %q, want the CJK label kept", got)
	}
	if !strings.Contains(got, msg) {
		t.Fatalf("feedback = %q, want the CJK error kept", got)
	}
}

// TestToolErrorReflectionLongMessageBounded 检查超长错误正文被裁剪到有界长度，
// 带截断标记，切点落在 rune 边界上（CJK 安全），且保留最近的尾部内容。
func TestToolErrorReflectionLongMessageBounded(t *testing.T) {
	// 前段用独特的 "DROP" 标记，后段填满接近预算的中文正文；
	// 这样有界窗口完全落在中文尾部，可直接断言前段已被丢弃。
	out := strings.Repeat("DROP", 100) + strings.Repeat("界", toolErrorReflectionMaxBytes/3+10) + "END_TAIL"
	got := toolErrorReflection("edit", out)

	prefix := toolErrorFixInstruction + "\n[tool: EDIT] ERROR\n"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("feedback = %q, want prefix %q", got, prefix)
	}
	body := strings.TrimPrefix(got, prefix)
	if len(body) > toolErrorReflectionMaxBytes {
		t.Fatalf("body = %d bytes, want <= %d", len(body), toolErrorReflectionMaxBytes)
	}
	if !strings.HasPrefix(body, verifyTruncatedMarker) {
		t.Fatalf("body = %q, want truncation marker", body)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("feedback is not valid UTF-8: %q", got)
	}
	if !strings.Contains(body, "界") || !strings.Contains(body, "END_TAIL") {
		t.Fatalf("body = %q, want the CJK runes and END_TAIL kept", body)
	}
	if strings.Contains(body, "DROP") {
		t.Fatalf("body = %q, want the leading bytes dropped", body)
	}
}

// TestToolErrorReflectionEmptyIsEmpty 检查空错误（含仅空白）不会产生无内容的反射反馈，
// 使调用方可以据此跳过注入。
func TestToolErrorReflectionEmptyIsEmpty(t *testing.T) {
	for _, err := range []string{"", "   ", "\n\t ", "  \r\n"} {
		if got := toolErrorReflection("edit", err); got != "" {
			t.Fatalf("toolErrorReflection(%q) = %q, want empty", err, got)
		}
	}
}

// TestToolErrorReflectionEmptyLabelFallback 检查空标签回退为默认类别名。
func TestToolErrorReflectionEmptyLabelFallback(t *testing.T) {
	got := toolErrorReflection("  ", "boom")
	want := toolErrorFixInstruction + "\n[tool: " + toolErrorLabelFallback + "] ERROR\nboom"
	if got != want {
		t.Fatalf("feedback = %q, want %q", got, want)
	}
}

// TestToolErrorReflectionRedactsSecrets 检查错误正文在包装前先经脱敏，
// 凭据不会随反射反馈泄漏。
func TestToolErrorReflectionRedactsSecrets(t *testing.T) {
	got := toolErrorReflection("edit", "api_key=sk_live_abcdefghijklmnop failed")
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("feedback leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("feedback = %q, want a [redacted] marker", got)
	}
}

// TestToolErrorReflectionFormatStable 检查重复调用渲染出逐字节相同的反馈。
func TestToolErrorReflectionFormatStable(t *testing.T) {
	const err = "编译失败：锚点不存在 at f.go:7"
	first := toolErrorReflection("edit", err)
	for i := 0; i < 8; i++ {
		if got := toolErrorReflection("edit", err); got != first {
			t.Fatalf("call %d = %q, want %q", i, got, first)
		}
	}
}

// TestAugmentToolErrorReflectionOptIn 检查接线门控默认关闭时逐字节透传，
// 仅在显式启用且输入确实是错误时才包装，且不会影响非错误结果。
func TestAugmentToolErrorReflectionOptIn(t *testing.T) {
	t.Cleanup(func() { SetToolErrorReflection(false) })
	SetToolErrorReflection(false)

	const raw = "error: bad arguments: cannot parse JSON"
	if got := AugmentToolErrorReflection("edit", raw, true); got != raw {
		t.Fatalf("disabled gate = %q, want raw output %q", got, raw)
	}

	SetToolErrorReflection(true)
	if got := AugmentToolErrorReflection("edit", raw, true); got != toolErrorReflection("edit", raw) {
		t.Fatalf("enabled gate = %q, want wrapped feedback", got)
	}
	if got := AugmentToolErrorReflection("edit", "file updated", false); got != "file updated" {
		t.Fatalf("success output = %q, want unchanged", got)
	}
	if got := AugmentToolErrorReflection("edit", "  ", true); got != "  " {
		t.Fatalf("empty error = %q, want unchanged", got)
	}
}
