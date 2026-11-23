package bash

import "context"

// 本文件提供安全咨询谓词的注入点。背景：内建 shell 早已在
// Options.Safety 上支持"调用方注入谓词"，但生产接线在早期版本里用的是
// 进程级 atomic.Value 全局，导致同一进程内并发的 Execute（TUI + RPC、或
// 多个会话）会互相覆盖谓词：运行 A 的 bash 调用拿到运行 B 的 chain/ctx，
// 审计写错 sessionID，甚至用已取消的 ctx 误拒。
//
// 现在谓词随 context 传播（仿 agent.WithBackgroundResults 的注入范式），
// 由 app.Execute 在每次运行时用自己的 ctx 调用 WithSafetyOracle 注入。
// 每个运行持有独立 ctx，天然隔离；运行结束 ctx 失效，谓词随之消失，
// 不存在进程级残留。shell 自身不 import security（分层规则），只读取 ctx
// 中的函数值并调用。

// safetyOracleCtxKey 是安全咨询谓词在 context 中的私有键类型。
// 用私有类型而非字符串，避免与其他包的 ctx 值发生键碰撞。
type safetyOracleCtxKey struct{}

// WithSafetyOracle 把本次运行的安全咨询谓词注入 ctx。
//
// 返回的 ctx 把 fn 挂在值链上，随 agent 循环一路传到 bash 工具。
// 并发的 Execute 各自注入到各自的 ctx，互不覆盖；fn == nil 时是空操作，
// 保持"未注入时默认放行"的既有语义。
func WithSafetyOracle(ctx context.Context, fn func(string) bool) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, safetyOracleCtxKey{}, fn)
}

// safetyOracleFrom 从 ctx 读取当前运行注入的安全咨询谓词；未注入时返回 nil。
// ctx 为 nil 也返回 nil（Interp.Ctx 允许为 nil）。
func safetyOracleFrom(ctx context.Context) func(string) bool {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(safetyOracleCtxKey{}).(func(string) bool)
	return fn
}

// CurrentSafety 给出 command 的最终安全判定。
//
// 优先级：显式传入的 fn 高于 ctx 中注入的 oracle。
//   - fn != nil：直接调用 fn(command)，忽略 ctx 中的 oracle；
//   - fn == nil 且 ctx 未注入 oracle：返回 true（放行，默认行为不变）；
//   - fn == nil 且 ctx 已注入 oracle：返回 oracle(command)。
//
// 约定：本函数不修改 command（Go 字符串不可变），也不修改 fn/oracle；
// 它原样把 command 交给谓词，UTF-8/CJK 文本按字节透传，绝不在 rune 中间
// 截断。oracle 返回 false 即代表拒绝，CurrentSafety 原样返回该 false。
func CurrentSafety(ctx context.Context, fn func(string) bool, command string) bool {
	if fn == nil {
		fn = safetyOracleFrom(ctx)
	}
	if fn == nil {
		return true
	}
	return fn(command)
}
