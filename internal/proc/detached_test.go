package proc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeChild 是一个无需启动真实 OS 进程，就能被告知存活到超时之后的
// 假进程。
type fakeChild struct {
	pid  int
	code int
	// finishNow 让 Wait 立即完成；否则它等待 done。
	finishNow bool
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	killed    bool
	proc      *Process
}

func newFakeChild(pid, code int, finishNow bool) *fakeChild {
	return &fakeChild{pid: pid, code: code, finishNow: finishNow, done: make(chan struct{})}
}

func (f *fakeChild) Pid() int { return f.pid }

func (f *fakeChild) Wait() int {
	if f.finishNow {
		_, _ = f.proc.Write([]byte("fake-output\n"))
		f.closeOnce.Do(func() { close(f.done) })
		return f.code
	}
	<-f.done
	return f.code
}

func (f *fakeChild) Kill() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.killed {
		f.killed = true
		f.closeOnce.Do(func() { close(f.done) })
	}
}

func (f *fakeChild) wasKilled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.killed
}

// installFakeStarter 换入一个返回单个假进程的启动器。
// 返回的指针让测试在 RunWithTimeout 之后检查假进程看到了什么。
func installFakeStarter(t *testing.T, child *fakeChild, seed string) **Process {
	t.Helper()
	var p *Process
	SetStarter(func(Command) (*Process, error) {
		p = NewProcess(child)
		child.proc = p
		if seed != "" {
			_, _ = p.Write([]byte(seed))
		}
		return p, nil
	})
	t.Cleanup(func() { SetStarter(nil) })
	return &p
}

func TestRunWithTimeoutKeepsChildAliveWhenKeepOnTimeout(t *testing.T) {
	child := newFakeChild(4242, 7, false)
	slot := installFakeStarter(t, child, "partial-from-fake\n")

	res, live, err := RunWithTimeout(context.Background(), Command{
		Path:          "fake",
		Timeout:       15 * time.Millisecond,
		KeepOnTimeout: true,
	})
	var timeoutErr *TimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want *TimeoutError", err)
	}
	if !errors.Is(err, ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want both ErrTimeout and DeadlineExceeded", err)
	}
	if live == nil || live != *slot {
		t.Fatalf("live = %v, want the started process", live)
	}
	if live.Pid() != 4242 {
		t.Fatalf("pid = %d, want 4242", live.Pid())
	}
	if child.wasKilled() {
		t.Fatal("KeepOnTimeout must not kill the child")
	}
	if !strings.Contains(res.Combined(), "partial-from-fake") {
		t.Fatalf("result lost the partial output: %q", res.Combined())
	}
	if !strings.Contains(live.Output(), "partial-from-fake") {
		t.Fatalf("live output lost the partial bytes: %q", live.Output())
	}

	// 被提升的子进程仍存活；通过其句柄终止可回收它。
	live.Kill()
	select {
	case <-live.Done():
	case <-time.After(time.Second):
		t.Fatal("child never finished after Kill")
	}
	if live.ExitCode() != 7 {
		t.Fatalf("exit code = %d, want 7", live.ExitCode())
	}
}

func TestRunWithTimeoutKillsTreeOnTimeoutByDefault(t *testing.T) {
	child := newFakeChild(9, 4, false)
	installFakeStarter(t, child, "")

	res, live, err := RunWithTimeout(context.Background(), Command{
		Path:    "fake",
		Timeout: 15 * time.Millisecond,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if live != nil {
		t.Fatalf("live = %v, want nil without KeepOnTimeout", live)
	}
	if !child.wasKilled() {
		t.Fatal("default timeout must still kill the tree")
	}
	if res.ExitCode == 0 {
		t.Fatalf("a timed-out child must not report a clean exit: %+v", res)
	}
}

func TestRunWithTimeoutReturnsOutputAndExitCodeForAFastCommand(t *testing.T) {
	child := newFakeChild(11, 3, true)
	installFakeStarter(t, child, "")

	res, live, err := RunWithTimeout(context.Background(), Command{Path: "fake", Timeout: time.Second})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if live != nil {
		t.Fatalf("live = %v, want nil for a finished command", live)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "fake-output") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

// expiredContext 确定性地模拟狭窄的 timeout/ctx 竞争：
// 其 Err() 已非 nil（调用方截止时间已到），而 Done()
// 仍开放，因此 select 只能选择软超时分支。这正是
// RunWithTimeout 的平局裁决必须处理的状态——真实 context 的
// 截止时间比定时器早一瞬触发时，到执行超时分支时
// 会产生相同的 Err() 值。
type expiredContext struct{}

func (expiredContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (expiredContext) Done() <-chan struct{}       { return nil }
func (expiredContext) Err() error                  { return context.DeadlineExceeded }
func (expiredContext) Value(any) any               { return nil }

// TestRunWithTimeoutContextDeadlineBeatsCoincidentTimeout 守护
// RunWithTimeout 中的 timeout/ctx 竞争。c.Timeout 是软性的"提升为
// 后台作业"截止时间，而 ctx 是调用方的硬界限；两者
// 几乎同时触发时，select 可能选择定时器，即使 context
// 已经结束。修复之前，KeepOnTimeout 随后返回一个活过调用方硬
// 截止时间的子进程（runPromotableBash 的 runCtx 正是
// 这样的调用方），把有界调用变成失控的作业。
func TestRunWithTimeoutContextDeadlineBeatsCoincidentTimeout(t *testing.T) {
	child := newFakeChild(4242, 9, false)
	slot := installFakeStarter(t, child, "")

	_, live, err := RunWithTimeout(expiredContext{}, Command{
		Path:          "fake",
		Timeout:       15 * time.Millisecond,
		KeepOnTimeout: true,
	})

	var te *TimeoutError
	if errors.As(err, &te) {
		t.Fatalf("err = %v, want a context deadline error, not *TimeoutError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if live != nil {
		t.Fatalf("live = %v, want nil: a finished context must not promote a child", live)
	}
	if !child.wasKilled() {
		t.Fatal("child was not killed past the caller's hard deadline")
	}
	_ = slot
}

func TestProcessSetOutputFlushesCapturedBytesAndRedirects(t *testing.T) {
	child := newFakeChild(12, 0, false)
	p := NewProcess(child)
	if _, err := p.Write([]byte("before\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var buf bytes.Buffer
	p.SetOutput(&buf)
	if !strings.Contains(buf.String(), "before") {
		t.Fatalf("handoff lost the partial output: %q", buf.String())
	}
	if _, err := p.Write([]byte("after\n")); err != nil {
		t.Fatalf("write after handoff: %v", err)
	}
	if !strings.Contains(buf.String(), "after") {
		t.Fatalf("later output was not redirected: %q", buf.String())
	}
}
