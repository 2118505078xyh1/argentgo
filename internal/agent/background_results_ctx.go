package agent

import "context"

// 本文件让后台结果队列的生命周期由 app 层的 runner 决定（per-session 持久），
// 而不必改动 execute 的函数签名——许多测试注入伪 execute，改签名会波及全局。
// agent 包只提供中立的 ctx 载体，不引用 app。

// bgResultsCtxKey 是后台结果队列在 context 中的私有键类型。
type bgResultsCtxKey struct{}

// WithBackgroundResults 把后台结果队列注入 ctx，使同一会话的多个提示共享一个
// 队列。后台任务可能比触发它的提示活得久（提示返回时调用方 ctx 被取消，但
// 子级用 WithoutCancel 继续运行）；共享队列让它的结果能在后续提示中被抽取。
// nil 队列是空操作。
func WithBackgroundResults(ctx context.Context, bg *BackgroundResults) context.Context {
	if bg == nil {
		return ctx
	}
	return context.WithValue(ctx, bgResultsCtxKey{}, bg)
}

// BackgroundResultsFrom 从 ctx 读取后台结果队列；未注入时返回 nil。
// 直接调用 Execute（eval、测试等）没有 runner 注入，会据此回退到一次性队列。
func BackgroundResultsFrom(ctx context.Context) *BackgroundResults {
	if ctx == nil {
		return nil
	}
	bg, _ := ctx.Value(bgResultsCtxKey{}).(*BackgroundResults)
	return bg
}
