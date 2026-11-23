package app

import (
	"encoding/json"
	"testing"
)

func TestApprovalPreviewEnabled(t *testing.T) {
	if !approvalPreviewEnabled(func(string) string { return "" }) {
		t.Fatal("default on")
	}
	if !approvalPreviewEnabled(func(string) string { return "1" }) {
		t.Fatal("env=1 on")
	}
	if approvalPreviewEnabled(func(string) string { return "0" }) {
		t.Fatal("env=0 off")
	}
	if !approvalPreviewEnabled(nil) {
		t.Fatal("nil env defaults on")
	}
}

func TestAuditSanitizeEnabled(t *testing.T) {
	// 安全默认值：脱敏先于审计写入是 AGENTS.md 不变量，默认必须开启。
	if !auditSanitizeEnabled(nil) {
		t.Fatal("nil env defaults on (security invariant)")
	}
	if !auditSanitizeEnabled(func(string) string { return "" }) {
		t.Fatal("default on")
	}
	if !auditSanitizeEnabled(func(string) string { return "1" }) {
		t.Fatal("env=1 on")
	}
	if auditSanitizeEnabled(func(string) string { return "0" }) {
		t.Fatal("env=0 off (emergency fallback)")
	}
	if auditSanitizeEnabled(func(string) string { return " 0 " }) {
		t.Fatal("env=0 with whitespace off")
	}
}

func TestApprovalDiffPreview(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"old": "a\nb\n", "new": "a\nc\nd\n"})
	got := approvalDiffPreview("edit", raw)
	if got == "" || got[:1] != "+" {
		t.Fatalf("preview = %q", got)
	}
	// 只读工具不附加预览。
	if approvalDiffPreview("read", raw) != "" {
		t.Fatal("read should have no preview")
	}
	// write 用 content。
	raw2, _ := json.Marshal(map[string]any{"content": "x\ny\n"})
	if got := approvalDiffPreview("write", raw2); got == "" {
		t.Fatal("write preview missing")
	}
}
