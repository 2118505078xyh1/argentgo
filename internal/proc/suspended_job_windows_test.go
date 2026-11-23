//go:build windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSuspendedJobEnabled 验证门控只认修剪后的精确 "1"，默认关闭。
func TestSuspendedJobEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want bool
	}{
		{name: "unset", env: "", want: false},
		{name: "one", env: "1", want: true},
		{name: "padded", env: " \t1 ", want: true},
		{name: "zero", env: "0", want: false},
		{name: "true", env: "true", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_SUSPENDED_JOB", tc.env)
			if got := suspendedJobEnabled(); got != tc.want {
				t.Fatalf("suspendedJobEnabled(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestShouldSuspendedStart 验证门控与 killTree 必须同时成立。
func TestShouldSuspendedStart(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		killTree bool
		want     bool
	}{
		{name: "gate off killTree", env: "0", killTree: true, want: false},
		{name: "gate on killTree", env: "1", killTree: true, want: true},
		{name: "gate on no killTree", env: "1", killTree: false, want: false},
		{name: "gate off no killTree", env: "", killTree: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_SUSPENDED_JOB", tc.env)
			if got := shouldSuspendedStart(tc.killTree); got != tc.want {
				t.Fatalf("shouldSuspendedStart(%v) = %v, want %v", tc.killTree, got, tc.want)
			}
		})
	}
}

// TestMarkSuspendedStartOnlyWhenEnabledAndKillTree 验证 CREATE_SUSPENDED
// 只在门控生效且需要整树终止时被追加，默认路径的 SysProcAttr 不受影响。
func TestMarkSuspendedStartOnlyWhenEnabledAndKillTree(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		killTree bool
		want     bool
	}{
		{name: "disabled", env: "0", killTree: true, want: false},
		{name: "enabled killTree", env: "1", killTree: true, want: true},
		{name: "enabled no killTree", env: "1", killTree: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ARGENT_SUSPENDED_JOB", tc.env)
			cmd := exec.Command("cmd.exe", "/C", "echo hi")
			markSuspendedStart(cmd, tc.killTree)
			got := cmd.SysProcAttr != nil && cmd.SysProcAttr.CreationFlags&createSuspended != 0
			if got != tc.want {
				t.Fatalf("suspend flag = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAttachSuspendedFailurePaths 用注入的假函数覆盖入作业失败路径：
// 子进程从未恢复运行，且必须被终止，作业句柄必须被关闭。
func TestAttachSuspendedFailurePaths(t *testing.T) {
	errCreate := errors.New("create failed")
	errAssign := errors.New("assign failed")
	errResume := errors.New("resume failed")

	cases := []struct {
		name          string
		createErr     error
		assignErr     error
		resumeErr     error
		wantErr       error
		wantJob       syscall.Handle
		wantTerminate int
		wantResume    int
		wantClose     int
	}{
		{name: "success", wantJob: syscall.Handle(0x1234), wantResume: 1, wantClose: 0, wantTerminate: 0},
		{name: "create fails", createErr: errCreate, wantErr: errCreate, wantJob: 0, wantTerminate: 1, wantResume: 0, wantClose: 0},
		{name: "assign fails", assignErr: errAssign, wantErr: errAssign, wantJob: 0, wantTerminate: 1, wantResume: 0, wantClose: 1},
		{name: "resume fails", resumeErr: errResume, wantErr: errResume, wantJob: 0, wantTerminate: 1, wantResume: 1, wantClose: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var terminated, resumed, closed int
			job, err := attachSuspended(
				4242,
				func() (syscall.Handle, error) {
					if tc.createErr != nil {
						return 0, tc.createErr
					}
					return syscall.Handle(0x1234), nil
				},
				func(syscall.Handle, int) error { return tc.assignErr },
				func(int) error {
					resumed++
					return tc.resumeErr
				},
				func(int) { terminated++ },
				func(syscall.Handle) { closed++ },
			)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if job != tc.wantJob {
				t.Fatalf("job = %v, want %v", job, tc.wantJob)
			}
			if terminated != tc.wantTerminate || resumed != tc.wantResume || closed != tc.wantClose {
				t.Fatalf("calls: terminate=%d resume=%d close=%d; want %d/%d/%d",
					terminated, resumed, closed, tc.wantTerminate, tc.wantResume, tc.wantClose)
			}
		})
	}
}

// TestSuspendedJobStartKillsGrandchildren 启动一个会派生孙进程的 helper，
// 走挂起启动路径后终止整棵树，验证父与孙都不再存活。
func TestSuspendedJobStartKillsGrandchildren(t *testing.T) {
	t.Setenv("ARGENT_SUSPENDED_JOB", "1")
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperKillGroup$")
	cmd.Env = append(os.Environ(),
		"ARGENT_PROC_HELPER=parent",
		"ARGENT_PROC_HELPER_PIDFILE="+pidFile,
	)

	handle, err := startTracked(cmd, true)
	if err != nil {
		t.Fatalf("startTracked: %v", err)
	}
	parentPid := cmd.Process.Pid
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	defer func() {
		handle.killTree()
		handle.close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		killPidForTest(parentPid)
	}()

	childPid := waitForPidFile(t, pidFile, 5*time.Second)
	if childPid == 0 {
		t.Fatal("grandchild pid was not written")
	}
	defer killPidForTest(childPid)

	handle.killTree()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not exit after killTree")
	}

	// 给终止传播留一点时间。
	time.Sleep(200 * time.Millisecond)
	if p, err := os.FindProcess(childPid); err == nil && processAlive(p) {
		t.Fatalf("grandchild %d survived killTree", childPid)
	}
	if p, err := os.FindProcess(parentPid); err == nil && processAlive(p) {
		t.Fatalf("parent %d survived killTree", parentPid)
	}
}

func waitForPidFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
				return n
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0
}

func killPidForTest(pid int) {
	if pid <= 0 {
		return
	}
	p, err := os.FindProcess(pid)
	if err == nil && processAlive(p) {
		_ = exec.Command("taskkill", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
}
