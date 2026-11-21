//go:build !windows

package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	graceHelperOnEnv        = "ARGENT_PROC_GRACE_HELPER"
	graceHelperModeEnv      = "ARGENT_PROC_GRACE_MODE"
	graceHelperFileEnv      = "ARGENT_PROC_GRACE_FILE"
	graceHelperGrandFileEnv = "ARGENT_PROC_GRACE_GRAND_FILE"
)

// TestProcGraceHelper 是一个重新执行的测试二进制，扮演被测的子进程。
// 在正常运行中它是空操作；测试框架设置标记环境变量
// 把它变成辅助进程。日志文件是握手通道：
// 子进程在安装好信号处理时追加 READY，若它在被终止前
// 真正捕获到 SIGTERM 则追加 GRACEFUL。
func TestProcGraceHelper(t *testing.T) {
	if os.Getenv(graceHelperOnEnv) == "" {
		return
	}
	f, err := os.OpenFile(os.Getenv(graceHelperFileEnv), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(2)
	}
	// 不用 defer：下面每条路径都以 os.Exit 结束，defer 本来也会被跳过。
	// os.File 写入是无缓冲的，因此标记会立即落盘。

	switch os.Getenv(graceHelperModeEnv) {
	case "graceful":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		_, _ = f.WriteString("READY\n")
		<-ch
		_, _ = f.WriteString("GRACEFUL\n")
		os.Exit(0)
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
		_, _ = f.WriteString("READY\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "spawn-ignore-grandchild":
		// 直接子进程派生一个忽略 SIGTERM 的孙进程，二者共用进程组。
		// 直接子进程自己在 SIGTERM 下优雅退出，触发"宽限内退出即返回"
		// 的旧分支；孙进程若无人补刀就会成为孤儿。
		gc := exec.Command(os.Args[0], "-test.run=^TestProcGraceHelper$")
		gc.Env = append(os.Environ(),
			graceHelperOnEnv+"=1",
			graceHelperModeEnv+"=orphan-grandchild",
			graceHelperFileEnv+"="+os.Getenv(graceHelperGrandFileEnv),
		)
		if err := gc.Start(); err != nil {
			os.Exit(2)
		}
		// 不等待孙进程：直接子进程很快退出，孙进程会被 init 接管。
		_ = gc.Process.Release()
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		_, _ = f.WriteString("READY\n")
		<-ch
		_, _ = f.WriteString("GRACEFUL\n")
		os.Exit(0)
	case "orphan-grandchild":
		// 孙进程忽略 SIGTERM，报告 PID 后长睡。只有 SIGKILL 能终止它。
		signal.Ignore(syscall.SIGTERM)
		_, _ = fmt.Fprintf(f, "READY %d\n", os.Getpid())
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func setCancellationGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := cancellationGracePeriod
	cancellationGracePeriod = d
	t.Cleanup(func() { cancellationGracePeriod = old })
}

type graceRun struct {
	res     Result
	err     error
	elapsed time.Duration
	log     string
}

// runGraceHelper 启动重新执行的子进程，等待其 READY 标记，
// 以确认信号处理器已安装，然后要么取消 context
// （cancel=true），要么让 Command.Timeout 触发（cancel=false）。
func runGraceHelper(t *testing.T, mode string, cancel bool, timeout time.Duration) graceRun {
	t.Helper()
	file := filepath.Join(t.TempDir(), "child.log")
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()

	cmd := Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestProcGraceHelper$"},
		Env: []string{
			graceHelperOnEnv + "=1",
			graceHelperModeEnv + "=" + mode,
			graceHelperFileEnv + "=" + file,
		},
		KillTree: true,
		Timeout:  timeout,
	}

	type outcome struct {
		res     Result
		err     error
		elapsed time.Duration
	}
	out := make(chan outcome, 1)
	go func() {
		started := time.Now()
		res, err := Run(ctx, cmd)
		out <- outcome{res: res, err: err, elapsed: time.Since(started)}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(file)
		if strings.Contains(string(b), "READY") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper never became ready (log=%q)", b)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if cancel {
		cancelFn()
	}

	select {
	case o := <-out:
		b, _ := os.ReadFile(file)
		return graceRun{res: o.res, err: o.err, elapsed: o.elapsed, log: string(b)}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the helper was signalled")
		return graceRun{}
	}
}

func TestCancelLetsGracefulChildExitBeforeSIGKILL(t *testing.T) {
	// 子进程配合 SIGTERM，因此 Run 应在它
	// 退出后立即返回——远早于宽限期结束、升级为 SIGKILL。
	// 在窗口之下留出整整一秒的调度余量：CI 运行器
	// （race 下的 ubuntu-latest）生成进程 + 文件握手可能需要约 1 秒，
	// 但仍明显早于 graceWindow 的 SIGKILL 路径。
	const graceWindow = 3 * time.Second
	setCancellationGrace(t, graceWindow)

	got := runGraceHelper(t, "graceful", true, 0)

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", got.err)
	}
	if !strings.Contains(got.log, "GRACEFUL") {
		t.Fatalf("child did not catch SIGTERM before the kill; log=%q", got.log)
	}
	if got.res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0 (graceful exit, not SIGKILL)", got.res.ExitCode)
	}
	// 优雅退出会在宽限期内很早返回；只有 SIGKILL
	// 升级路径可能耗时 graceWindow 或更长。一秒的余量
	// 使该测试对缓慢的 CI 调度保持稳健，同时不丢失这一信号。
	if got.elapsed >= graceWindow-time.Second {
		t.Fatalf("Run took %s; a graceful child must exit within the grace window (%s)", got.elapsed, graceWindow)
	}
}

func TestCancelEscalatesToSIGKILLAfterGraceWhenSIGTERMIgnored(t *testing.T) {
	setCancellationGrace(t, 300*time.Millisecond)

	got := runGraceHelper(t, "ignore", true, 0)

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", got.err)
	}
	if strings.Contains(got.log, "GRACEFUL") {
		t.Fatalf("an SIGTERM-ignoring child must not report graceful cleanup: %q", got.log)
	}
	if got.elapsed < 250*time.Millisecond {
		t.Fatalf("Run took %s; it must wait the grace period before escalating to SIGKILL", got.elapsed)
	}
	if got.res.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1 (killed by SIGKILL)", got.res.ExitCode)
	}
}

func TestTimeoutKillsImmediatelyWithoutGraceWait(t *testing.T) {
	// 超时路径必须完全忽略宽限期。这里设置一个很长的宽限
	// 期，能让任何意外的等待都显而易见。
	setCancellationGrace(t, 2*time.Second)

	got := runGraceHelper(t, "ignore", false, 150*time.Millisecond)

	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", got.err)
	}
	if got.elapsed >= time.Second {
		t.Fatalf("Run took %s; the timeout path must not wait for the grace period", got.elapsed)
	}
}

// TestCancelKillsOrphanGrandchildThatIgnoresSIGTERM 守护审计发现的孤儿缺口：
// 用户取消先向整棵树发 SIGTERM，直接子进程在宽限期内优雅退出，但孙进程
// 忽略 SIGTERM。若看到直接子进程退出就返回而不补刀，孙进程会残留为孤儿。
// 修复后应按判据升级为 killTree 清理整棵树。
func TestCancelKillsOrphanGrandchildThatIgnoresSIGTERM(t *testing.T) {
	setCancellationGrace(t, 400*time.Millisecond)

	dir := t.TempDir()
	childLog := filepath.Join(dir, "child.log")
	grandLog := filepath.Join(dir, "grandchild.log")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := Command{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestProcGraceHelper$"},
		Env: []string{
			graceHelperOnEnv + "=1",
			graceHelperModeEnv + "=spawn-ignore-grandchild",
			graceHelperFileEnv + "=" + childLog,
			graceHelperGrandFileEnv + "=" + grandLog,
		},
		KillTree: true,
	}

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, cmd)
		done <- err
	}()

	// 等直接子进程与孙进程都就绪再取消，确保 SIGTERM 到达时孙进程
	// 已经装好忽略处理器，且 PID 已被报告。
	deadline := time.Now().Add(5 * time.Second)
	var grandPID int
	for {
		cb, _ := os.ReadFile(childLog)
		gb, _ := os.ReadFile(grandLog)
		var ready string
		n, scanErr := fmt.Sscanf(string(gb), "%s %d", &ready, &grandPID)
		if strings.Contains(string(cb), "READY") && scanErr == nil && n == 2 && ready == "READY" && grandPID > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helpers never became ready (child=%q grand=%q)", cb, gb)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 兜底清理：即便断言失败也不把孙进程留在系统里。
	t.Cleanup(func() {
		if grandPID > 0 {
			_ = syscall.Kill(grandPID, syscall.SIGKILL)
		}
	})

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}

	cb, _ := os.ReadFile(childLog)
	if !strings.Contains(string(cb), "GRACEFUL") {
		t.Fatalf("direct child did not exit gracefully on SIGTERM: %q", cb)
	}
	if !grandchildGone(grandPID) {
		t.Fatalf("orphan grandchild pid %d survived cancellation; killTree was not applied after the direct child exited", grandPID)
	}
}

// grandchildGone 判断孙进程是否已消失。Linux 上被 SIGKILL 的孤立进程会
// 成为 init 的僵尸，kill(pid, 0) 仍返回成功，因此优先读 /proc/<pid>/stat
// 的进程状态：Z/X 视为已终止；无 /proc 的平台回退到信号 0 探测。
func grandchildGone(pid int) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !grandchildAlive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func grandchildAlive(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		// stat 形如 "123 (comm) S ..."；comm 可能含空格，从最后一个 ')' 之后取状态。
		if i := strings.LastIndex(string(stat), ")"); i >= 0 && i+2 < len(stat) {
			switch string(stat)[i+2] {
			case 'Z', 'X', 'x':
				return false
			}
		}
		return true
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
