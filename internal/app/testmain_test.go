package app

import (
	"os"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestMain 在 app 包的测试二进制内关闭审批对话框的防误触宽限期。
// 产品默认 250ms（见 tui.DialogKeyGracePeriod）用于防止尾随按键误批；
// 但 app 层的审批器测试需要在弹窗出现后立即发 y/n 并等待 resolve，
// 宽限期会让这些按键被吞掉导致 2 秒超时。tui 包已有同类 TestMain 处理，
// 这里对 app 包做同等隔离，不改变任何产品行为。
func TestMain(m *testing.M) {
	old := tui.DialogKeyGracePeriod
	tui.DialogKeyGracePeriod = 0
	code := m.Run()
	tui.DialogKeyGracePeriod = old
	os.Exit(code)
}
