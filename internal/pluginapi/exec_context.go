package pluginapi

import (
	"context"
	"time"
)

// execContextKey 是执行上下文的私有 key 类型。用未导出的空结构体确保
// 只有本包的 With/From 函数能读写，避免跨包复制 key 导致类型身份不匹配。
type execContextKey struct{}

// ExecContext 携带一次工具执行所需的运行时参数。定义在零依赖的 pluginapi，
// 使注册表持有方（tools）与工具实现方（shell）能共享同一份上下文，而不必
// 互相 import。Jobs 用 any 承载以避免 pluginapi 依赖具体作业包；消费方
// 通过类型断言还原（如 *jobs.Registry）。
type ExecContext struct {
	MaxOutput  int
	MaxTimeout time.Duration
	SessionDir string
	Jobs       any
	// Spill 是注册表注入的输出产物落盘回调：当超大输出需要 opt-in
	// 落盘时，工具实现方（如 internal/shell 的命令类工具）调用它；
	// handled 为 true 时 out 必须原样返回，否则回退普通裁剪路径。
	// 签名只用 stdlib 类型，使 pluginapi 保持零 internal 依赖；
	// 未注入时（默认）行为与迁移前逐字节一致。
	Spill func(text string) (out string, handled bool)
}

// WithExecContext 把执行参数写入 ctx。
func WithExecContext(ctx context.Context, ec ExecContext) context.Context {
	return context.WithValue(ctx, execContextKey{}, ec)
}

// ExecContextFrom 从 ctx 读出执行参数。未写入时返回零值 + false。
func ExecContextFrom(ctx context.Context) (ExecContext, bool) {
	ec, ok := ctx.Value(execContextKey{}).(ExecContext)
	return ec, ok
}
