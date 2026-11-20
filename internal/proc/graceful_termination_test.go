//go:build !windows

package proc

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	graceHelperOnEnv   = "ARGENT_PROC_GRACE_HELPER"
	graceHelperModeEnv = "ARGENT_PROC_GRACE_MODE"
	graceHelperFileEnv = "ARGENT_PROC_GRACE_FILE"
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
