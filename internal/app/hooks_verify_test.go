package app

import (
	"strings"
	"testing"
)

// TestAppendVerifyNoteAppendsPassVerdict 检查通过的验证注释是否作为
// 普通的 PostToolUse 风格钩子行附加。
func TestAppendVerifyNoteAppendsPassVerdict(t *testing.T) {
	got := AppendVerifyNote("tool out", " lint ", true, "noisy output")
	want := "tool out\n[hook] [verify: LINT] PASS"
	if got != want {
		t.Fatalf("AppendVerifyNote = %q, want %q", got, want)
	}
}

// TestAppendVerifyNoteEmptyOutputIsVerdictOnly 检查空输出上的注释只有结论，
// 没有空的输出部分。
func TestAppendVerifyNoteEmptyOutputIsVerdictOnly(t *testing.T) {
	got := AppendVerifyNote("", "test", false, "  \n\t ")
	want := "[hook] [verify: TEST] FAIL"
	if got != want {
		t.Fatalf("AppendVerifyNote = %q, want %q", got, want)
	}
}

// TestAppendVerifyNoteFailRedactsAndKeepsTail 检查失败的验证注释携带
// 命令输出经脱敏后的尾部，并保持工具自身的结果不变。
// （接上行）
func TestAppendVerifyNoteFailRedactsAndKeepsTail(t *testing.T) {
	cmdOutput := "api_key=sk_live_abcdefghijklmnop failed at step 3"
	got := AppendVerifyNote("edit result", "test", false, cmdOutput)

	if !strings.HasPrefix(got, "edit result\n[hook] [verify: TEST] FAIL\n") {
		t.Fatalf("note = %q, want the verdict after the tool output", got)
	}
	if strings.Contains(got, "sk_live_abcdefghijklmnop") {
		t.Fatalf("note leaked the secret: %q", got)
	}
	if !strings.Contains(got, "[redacted]") || !strings.HasSuffix(got, "failed at step 3") {
		t.Fatalf("note = %q, want a redacted output tail", got)
	}
}
