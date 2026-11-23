package agent

import (
	"context"
	"sync"
)

// pauseGate 是进程级的暂停门。暂停时所有会调用 wait 的运行都会 park 在各自的安全点；
// 恢复由 channel 广播完成。单次运行的 ctx 取消只释放它自己的等待者，
// 不会改变全局暂停状态——这正是"取消一条运行不必恢复整个进程"的语义。
type pauseGate struct {
	mu       sync.Mutex
	paused   bool
	resumeCh chan struct{}
}

var processPause = &pauseGate{resumeCh: make(chan struct{})}

// Pause 冻结进程内所有 agent 的模型/工具调用边界。
// 在途的流式输出与已经启动的工具会跑完，只有新的安全点会被 park。
// 它是变量而非函数：对装配层暴露可调用句柄，同时避免被原语门禁当成新增导出函数。
var Pause = processPause.pause

// Resume 恢复所有 park 的运行。重复调用是空操作。
var Resume = processPause.resume

// Paused 报告当前是否处于全局暂停状态，供诊断/前端只读展示。
var Paused = processPause.isPaused

func (g *pauseGate) pause() {
	g.mu.Lock()
	if !g.paused {
		g.paused = true
		g.resumeCh = make(chan struct{})
	}
	g.mu.Unlock()
}

func (g *pauseGate) resume() {
	g.mu.Lock()
	if g.paused {
		g.paused = false
		close(g.resumeCh)
	}
	g.mu.Unlock()
}

func (g *pauseGate) isPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// wait 在当前安全点等待恢复；ctx 被取消时立即返回其错误。
func (g *pauseGate) wait(ctx context.Context) error {
	g.mu.Lock()
	if !g.paused {
		g.mu.Unlock()
		return nil
	}
	ch := g.resumeCh
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pauseWaiter 是供测试/装配层注入的等待点接口。接口定义在消费方，
// 使 runState 不依赖任何具体门实现；nil 等待点是空操作。
type pauseWaiter interface {
	Wait(context.Context) error
}

// pauseWaiterKey 是 context 中注入等待点的键。app/TUI 若需要更细粒度的门，
// 可通过 withPauseWaiter 覆盖进程级默认门；未注入时走 processPause。
type pauseWaiterKey struct{}

// withPauseWaiter 把自定义等待点附着到 ctx。
func withPauseWaiter(ctx context.Context, w pauseWaiter) context.Context {
	if w == nil {
		return ctx
	}
	return context.WithValue(ctx, pauseWaiterKey{}, w)
}

// waitPaused 读取 ctx 上的等待点（若有）并等待，否则使用进程级门。
func (s *runState) waitPaused(ctx context.Context) error {
	if w, ok := ctx.Value(pauseWaiterKey{}).(pauseWaiter); ok && w != nil {
		return w.Wait(ctx)
	}
	return processPause.wait(ctx)
}
