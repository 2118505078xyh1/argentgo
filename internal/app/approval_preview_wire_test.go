package app

import (
	"encoding/json"
	"testing"
)

func TestApprovalPreviewEnabled(t *testing.T) {
	if approvalPreviewEnabled(func(string) string { return "" }) {
		t.Fatal("default off")
	}
	if !approvalPreviewEnabled(func(string) string { return "1" }) {
		t.Fatal("env=1 on")
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
