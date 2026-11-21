package proc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// signallingChild 是记录 terminate/kill 请求的假子进程。当设置 cooperate 时，
// 它在 TerminateTree 上关闭等待通道，模拟在优雅停止信号下
// 刷新并退出的子进程。
type signallingChild struct {
	pid        int
	cooperate  bool
	done       chan struct{}
	once       sync.Once
	mu         sync.Mutex
	terminated bool
	killed     bool
	ops        []string // 按发生顺序记录 terminate/kill，验证优雅终止先于补刀
}

func (f *signallingChild) Pid() int { return f.pid }

func (f *signallingChild) Wait() int {
	<-f.done
	return 0
}

func (f *signallingChild) Kill() {
	f.mu.Lock()
	f.killed = true
	f.ops = append(f.ops, "kill")
	f.mu.Unlock()
	f.finish()
}

func (f *signallingChild) TerminateTree() {
	f.mu.Lock()
	f.terminated = true
	f.ops = append(f.ops, "terminate")
	f.mu.Unlock()
	if f.cooperate {
		f.finish()
	}
}

func (f *signallingChild) finish() {
	f.once.Do(func() { close(f.done) })
}

func (f *signallingChild) requests() (terminated, killed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.terminated, f.killed
}

// opsSnapshot 返回已记录操作的副本，供断言操作顺序。
func (f *signallingChild) opsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

func installSignallingStarter(t *testing.T, child *signallingChild) {
	t.Helper()
	SetStarter(func(Command) (*Process, error) {
		return NewProcess(child), nil
	})
	t.Cleanup(func() { SetStarter(nil) })
}

// TestRunWithTimeoutUserCancelTerminatesTreeBeforeEscalating 守护这样的缺口：
// 分离生成路径曾直接杀死被取消的子进程，而 Run
// 会给它一个 SIGTERM 宽限期。忽略终止请求的子进程
// 仍必须被杀死，但只能在宽限窗口之后。
func TestRunWithTimeoutUserCancelTerminatesTreeBeforeEscalating(t *testing.T) {
	old := cancellationGracePeriod
	cancellationGracePeriod = 80 * time.Millisecond
	defer func() { cancellationGracePeriod = old }()

	child := &signallingChild{pid: 1234, done: make(chan struct{})}
	installSignallingStarter(t, child)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	res, live, err := RunWithTimeout(ctx, Command{Path: "fake"})
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	if live != nil {
		t.Fatalf("live = %v, want nil after a cancellation", live)
	}
	if res.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", res.ExitCode)
	}
	terminated, killed := child.requests()
	if !terminated {
		t.Fatal("child was never asked to terminate gracefully")
	}
	if !killed {
		t.Fatal("child that ignored the terminate request was not escalated to Kill")
	}
	if elapsed < 60*time.Millisecond {
		t.Fatalf("RunWithTimeout took %s; it must wait the grace period before escalating", elapsed)
	}
}

// TestRunWithTimeoutUserCancelStopsAtGracefulExit 验证宽限期的意义：
// 协作式子进程收到 SIGTERM 后自行退出，RunWithTimeout 应在宽限期内
// 立即返回，而不是等满整个宽限期。
//
// 直接子进程退出后仍会按判据补杀整棵树（用于清理忽略 SIGTERM 的孙进程），
// 因此对已退出的直接子进程仍会调用 Kill。这里守护的是"优雅终止必须先于
// 任何补刀"：context 取消不得绕过 SIGTERM 直接杀进程。
func TestRunWithTimeoutUserCancelStopsAtGracefulExit(t *testing.T) {
	old := cancellationGracePeriod
	cancellationGracePeriod = 3 * time.Second
	defer func() { cancellationGracePeriod = old }()

	child := &signallingChild{pid: 1235, cooperate: true, done: make(chan struct{})}
	installSignallingStarter(t, child)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	_, _, err := RunWithTimeout(ctx, Command{Path: "fake"})
	elapsed := time.Since(started)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want Canceled", err)
	}
	terminated, _ := child.requests()
	if !terminated {
		t.Fatal("child was never asked to terminate gracefully")
	}
	ops := child.opsSnapshot()
	if len(ops) == 0 || ops[0] != "terminate" {
		t.Fatalf("graceful terminate must precede any kill; ops = %v", ops)
	}
	if elapsed >= time.Second {
		t.Fatalf("RunWithTimeout took %s; it must return as soon as the child exits gracefully", elapsed)
	}
}

// TestRunWithTimeoutDeadlineStillKillsImmediately 保持超时语义
// 不变：两阶段停止只针对显式的用户取消。
func TestRunWithTimeoutDeadlineStillKillsImmediately(t *testing.T) {
	old := cancellationGracePeriod
	cancellationGracePeriod = 3 * time.Second
	defer func() { cancellationGracePeriod = old }()

	child := &signallingChild{pid: 1236, done: make(chan struct{})}
	installSignallingStarter(t, child)

	_, _, err := RunWithTimeout(expiredContext{}, Command{Path: "fake", Timeout: 10 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	terminated, killed := child.requests()
	if terminated {
		t.Fatal("a deadline must not use the graceful terminate path")
	}
	if !killed {
		t.Fatal("a deadline must still kill the child")
	}
}
