//go:build windows

package proc

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestKillGroupKillsGrandchildren 验证 Windows 作业对象能终结孙进程：
// PowerShell 启动后台 ping 并输出其 PID，KillGroup 之后该进程必须消失。
func TestKillGroupKillsGrandchildren(t *testing.T) {
	script := `$p = Start-Process -FilePath ping -ArgumentList '-n','30','127.0.0.1' -PassThru -WindowStyle Hidden; $p.Id; Wait-Process -Id $p.Id`
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	ConfigureGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		KillGroup(cmd.Process)
		_ = cmd.Wait()
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		KillGroup(cmd.Process)
		_ = cmd.Wait()
		t.Fatalf("parse grandchild pid %q: %v", line, err)
	}

	KillGroup(cmd.Process)
	_ = cmd.Wait()
	time.Sleep(100 * time.Millisecond)

	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid)).Output()
	if err != nil {
		t.Fatalf("tasklist: %v", err)
	}
	if strings.Contains(string(out), strconv.Itoa(pid)) {
		_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
		t.Fatalf("grandchild %d survived KillGroup", pid)
	}
}
