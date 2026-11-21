//go:build windows

package proc

import (
	"os/exec"
	"testing"
)

// TestWindowsRepeatedKillTreeDoesNotPanic 验证修复后的"直接子进程退出后
// 再补杀整棵树"在 Windows 上安全：terminateTree 在 Windows 上本身就执行
// killTree，close 会清空作业句柄，随后的二次 killTree 走 taskkill 回退
// 路径，不得 panic。
func TestWindowsRepeatedKillTreeDoesNotPanic(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/C", "ping -n 30 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动 cmd.exe: %v", err)
	}
	p := newPlatformProcess(cmd.Process, true)

	// 第一次：模拟两阶段取消的 terminateTree（Windows 上等同 killTree）。
	p.killTree()
	_ = cmd.Wait()

	// Done 路径会先调用 close 清空作业句柄；紧接着的补刀是修复后新增的调用。
	p.close()
	p.killTree()
	p.close()
}
