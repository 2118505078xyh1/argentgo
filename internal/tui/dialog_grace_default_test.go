package tui

import (
	"os"
	"testing"
	"time"
)

// TestMain 把审批按键宽限期固定为 0，使本包大量「弹窗出现后立即按键」的
// 确定性断言保持原样；产品默认值（250ms）由下面的测试直接校验解析器，
// dialog_grace_test.go 的用例也会各自显式设置所需宽限期。
func TestMain(m *testing.M) {
	DialogKeyGracePeriod = 0
	os.Exit(m.Run())
}

// TestDialogGraceDefaultIs250ms 锁定产品默认宽限期落在 200-300ms 区间，
// 且只有环境变量精确为 0 时才回退到关闭。
func TestDialogGraceDefaultIs250ms(t *testing.T) {
	old := dialogGraceEnv
	t.Cleanup(func() { dialogGraceEnv = old })

	dialogGraceEnv = func(string) string { return "" }
	if got := resolveDialogGrace(); got < 200*time.Millisecond || got > 300*time.Millisecond {
		t.Fatalf("default grace = %v, want 200-300ms", got)
	}
	dialogGraceEnv = func(string) string { return " 0 " }
	if got := resolveDialogGrace(); got != 0 {
		t.Fatalf("grace with ARGENT_DIALOG_GRACE_MS=0 = %v, want 0", got)
	}
	dialogGraceEnv = func(string) string { return "1" }
	if got := resolveDialogGrace(); got != defaultDialogGrace {
		t.Fatalf("grace with unrelated env = %v, want %v", got, defaultDialogGrace)
	}
}
