package proc

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// longRunningCommand 返回一个可移植的、运行约 30 秒的子进程命令。
func longRunningCommand() *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("ping", "-n", "30", "127.0.0.1")
	}
	return exec.Command("sleep", "30")
}

// TestConfigureGroupAndKillGroup 验证两个导出辅助的基本接线：
// ConfigureGroup 设置了平台进程组属性，KillGroup 能终止已启动的进程。
func TestConfigureGroupAndKillGroup(t *testing.T) {
	cmd := longRunningCommand()
	ConfigureGroup(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("ConfigureGroup did not set SysProcAttr")
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	KillGroup(cmd.Process)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("KillGroup did not terminate the process within 5s")
	}
}
