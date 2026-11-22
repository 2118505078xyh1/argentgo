//go:build !windows

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestKillGroupKillsGrandchildren 验证杀的是整个进程组而不只是直接子进程：
// sh 启动后台 sleep 并把它的 PID 写文件，KillGroup 之后该孙进程必须也消失。
func TestKillGroupKillsGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("sh", "-c", fmt.Sprintf("sleep 30 & echo $! > %q; wait", pidFile))
	ConfigureGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	var childPid int
	deadline := time.Now().Add(3 * time.Second)
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

	// 给信号传播留一点时间；若孙进程仍活着，先清理再报错。
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(childPid, 0); err == nil {
		_ = syscall.Kill(childPid, syscall.SIGKILL)
		t.Fatalf("grandchild %d survived KillGroup", childPid)
	}
}
