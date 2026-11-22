package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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

// TestHelperKillGroup 是进程树测试的辅助入口：用同一个测试二进制既做父进程
// 又做孙进程，避免依赖各平台 shell 的后台任务语义。
// 父 helper 启动孙 helper（它会把 PID 写入文件），两者都进入长时间睡眠。
func TestHelperKillGroup(t *testing.T) {
	mode := os.Getenv("ARGENT_PROC_HELPER")
	if mode == "" {
		return
	}
	if mode == "child" {
		pidFile := os.Getenv("ARGENT_PROC_HELPER_PIDFILE")
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	} else if mode == "parent" {
		pidFile := os.Getenv("ARGENT_PROC_HELPER_PIDFILE")
		child := exec.Command(os.Args[0], "-test.run=^TestHelperKillGroup$")
		child.Env = append(os.Environ(),
			"ARGENT_PROC_HELPER=child",
			"ARGENT_PROC_HELPER_PIDFILE="+pidFile,
		)
		_ = child.Start()
	}
	time.Sleep(60 * time.Second)
}

// TestKillGroupKillsGrandchildren 验证杀的是整个进程组而不只是直接子进程：
// KillGroup 之后，父 helper 启动的孙 helper 必须也消失。
func TestKillGroupKillsGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperKillGroup$")
	cmd.Env = append(os.Environ(),
		"ARGENT_PROC_HELPER=parent",
		"ARGENT_PROC_HELPER_PIDFILE="+pidFile,
	)
	ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	var childPid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				childPid = n
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPid == 0 {
		KillGroup(cmd.Process)
		_ = cmd.Wait()
		t.Fatal("grandchild pid was not written")
	}

	KillGroup(cmd.Process)
	_ = cmd.Wait()

	// 给终止传播留一点时间。
	time.Sleep(200 * time.Millisecond)
	grandchild, err := os.FindProcess(childPid)
	if err == nil && ProcessAlive(grandchild) {
		_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(childPid)).Run() // Windows 兜底清理
		t.Fatalf("grandchild %d survived KillGroup", childPid)
	}
}
