package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPauseGate(t *testing.T) {
	// 确保测试前后进程状态干净。
	processPause.resume()
	t.Cleanup(func() { processPause.resume() })

	if err := processPause.wait(context.Background()); err != nil {
		t.Fatalf("未暂停时 wait 应立即返回: %v", err)
	}

	processPause.pause()
	if !processPause.isPaused() {
		t.Fatal("pause 后应处于暂停态")
	}

	// 单次运行取消只解当前等待者，不改变全局状态。
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := processPause.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("取消后应返回 DeadlineExceeded, got %v", err)
	}
	if !processPause.isPaused() {
		t.Fatal("单运行取消不应恢复进程")
	}

	// 恢复后所有等待者一起放行。
	resumed := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { resumed <- processPause.wait(context.Background()) }()
	}
	time.Sleep(10 * time.Millisecond)
	select {
	case <-resumed:
		t.Fatal("暂停期间不应放行")
	default:
	}
	processPause.resume()
	for i := 0; i < 2; i++ {
		select {
		case err := <-resumed:
			if err != nil {
				t.Fatalf("恢复后 wait 返回错误: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("恢复后等待者未放行")
		}
	}
	if processPause.isPaused() {
		t.Fatal("resume 后不应仍暂停")
	}
}

// pauseWaiterStub 用于验证 ctx 注入的等待点优先于进程级门。
type pauseWaiterStub struct {
	err error
}

func (w pauseWaiterStub) Wait(context.Context) error { return w.err }

// TestPauseGateBlocksNewTool 验证工具启动前的暂停点：暂停期间新工具不会启动，
// 恢复后才执行；在途工具不受影响（由工具自身跑完的语义保证）。
func TestPauseGateBlocksNewTool(t *testing.T) {
	processPause.resume()
	processPause.pause()
	t.Cleanup(func() { processPause.resume() })

	var started atomic.Bool
	tools := &fakeTools{run: func(context.Context, string, string) (string, error) {
		started.Store(true)
		return "ok", nil
	}}
	s := newRunState(Options{Model: "m", Chatter: nil, Tools: tools}, "go", nil)
	call := toolCall("c1", "read", `{}`)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.runTool(context.Background(), call, time.Now())
	}()

	select {
	case <-done:
		t.Fatal("暂停期间工具不应启动")
	case <-time.After(30 * time.Millisecond):
	}
	if started.Load() {
		t.Fatal("暂停期间工具已启动")
	}
	processPause.resume()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("恢复后工具未执行")
	}
	if !started.Load() {
		t.Fatal("恢复后工具应执行")
	}
}

func TestPauseGateCtxWaiter(t *testing.T) {
	want := errors.New("injected pause")
	s := &runState{}
	ctx := withPauseWaiter(context.Background(), pauseWaiterStub{err: want})
	if err := s.waitPaused(ctx); !errors.Is(err, want) {
		t.Fatalf("注入等待点未生效: %v", err)
	}
}
