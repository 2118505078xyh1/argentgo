package llm

import "context"

// 本文件为"推测式执行"铺路：只定义工具调用增量的数据结构、context hook
// 与一个纯合并函数，不接入任何协议的 readStream，也不改动 Chatter 签名
// 或任何 wire 行为。
//
// 调研结论：三协议的 readStream 在内部累积工具参数却没有增量出口，而
// Chatter 接口有大量 fake 实现，不能改签名。因此把接缝做成可选 hook：
// 调用方通过 context 注入，llm 侧只提供纯函数，保持零 internal 依赖、
// 零调用方、零副作用。

// ToolCallDelta 是一次工具调用在流中的增量片段。
//
// 字段名与既有 ToolCall/ToolCallFunction 对齐（ID、Name、Arguments），
// 并额外携带 Index 以便在多路并发调用中定位所属槽位。它是纯数据，
// 不持有连接、不缓存、不依赖任何协议实现。
type ToolCallDelta struct {
	// Index 是该增量所属的调用槽位，对应 chat/anthropic 流中的 index
	// 或 responses 流中的 output_index。
	Index int
	// ID 是调用的稳定标识。流可能只在首片携带 ID，后续片为空，
	// 合并时取首个非空值。
	ID string
	// Name 是工具名。与 ID 一样通常只在首片出现，合并时取首个非空值。
	Name string
	// Arguments 是原始 JSON 参数的一个片段；各片按顺序拼接才是完整参数。
	Arguments string
}

// ToolDeltaHook 是工具调用增量的消费者。它由调用方通过 WithToolDeltaHook
// 注入，llm 侧只在明确调用 HookFromContext 并取得非空 hook 时才回调；
// 当前没有任何内置调用点，因此不影响既有 wire 行为。
type ToolDeltaHook func(ToolCallDelta)

// toolDeltaHookKey 是 context 中 hook 的私有键类型。采用零宽度的空结构体
// 而非字符串字面量，可避免与 agent/tools 中 "argent.tool.progress" 等
// 基于字符串的键相撞；只有持有该类型的包才能读写它。
type toolDeltaHookKey struct{}

// WithToolDeltaHook 在 ctx 中安装一个工具增量 hook。h 为 nil 时原样返回 ctx，
// 使调用方可以无条件传入。它不复制或修改 hook，也不产生任何即时调用。
func WithToolDeltaHook(ctx context.Context, h ToolDeltaHook) context.Context {
	if h == nil {
		return ctx
	}
	return context.WithValue(ctx, toolDeltaHookKey{}, h)
}

// HookFromContext 读取 ctx 中由 WithToolDeltaHook 安装的 hook。
// 未安装、被其它类型的值占用、或 ctx 为 nil 时返回 nil；调用方据此做空操作。
func HookFromContext(ctx context.Context) ToolDeltaHook {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(toolDeltaHookKey{}).(ToolDeltaHook)
	return h
}

// MergeToolDelta 把 delta 合并进 acc，返回新的增量值，不修改任何入参。
//
// 合并规则：
//   - Arguments 按到达顺序拼接（acc.Arguments + delta.Arguments）；
//   - ID/Name 取首个非空值：acc 已有值则保留，否则采用 delta 的值，
//     与 client.go "保留第一个 id" 的既有语义一致；
//   - Index 以 acc 为准。纯函数没有错误返回值，"一致性校验"体现为
//     delta 的 Index 不会覆盖 acc：即使网关在流中途重新编号，
//     累积器的槽位身份也保持稳定，参数不会被并入错误的调用。
func MergeToolDelta(acc, delta ToolCallDelta) ToolCallDelta {
	merged := acc
	merged.Arguments = acc.Arguments + delta.Arguments
	if merged.ID == "" {
		merged.ID = delta.ID
	}
	if merged.Name == "" {
		merged.Name = delta.Name
	}
	// merged.Index 保持 acc.Index，刻意不采纳 delta.Index。
	return merged
}
