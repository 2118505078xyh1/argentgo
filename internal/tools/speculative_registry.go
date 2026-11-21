package tools

import "context"

// 本文件提供推测执行在 tools 侧的原语：把 agent 预跑结果通过 context
// 旁路进 Registry.Run，以及构造一个供预跑使用的无 guard 注册表。
//
// 设计要点（与 agent/speculate.go 配套）：
//   - 预跑必须用独立的无 guard 注册表，否则真实调用与预跑都会触发审计/审批，
//     造成审计重复或缺失；NewSpeculativeRegistry 即为此准备。
//   - 查询旁路必须先经过 checkGuard 再决定是否短路，因此 Run 中的插入点
//     位于守卫之后、runTool 之前 —— 守卫（审计/审批）永远先执行。
//
// tools 不 import agent：接线层负责把 agent.SpecCache 适配成 SpecLookup
// （例如先算 SpeculativeFingerprint 再 Get），再用 WithSpecLookup 注入。
// 全部是 opt-in 原语，零内置调用方，默认 ctx 下无任何行为差异。

// SpecLookup 从推测缓存查询一次已预跑的工具调用结果。
//
// name 与 args 与 Registry.Run 收到的一致（args 已被规范化：空白参数为 "{}"）。
// ok 为 true 时 out 是可直接复用的输出，Run 跳过真正的工具执行并立即返回 out；
// ok 为 false 表示未命中，Run 继续走正常执行路径。
type SpecLookup func(name, args string) (string, bool)

// specLookupKey 是 WithSpecLookup / SpecLookupFrom 使用的类型化空 key。
// 用私有空结构体而非字符串，避免与其他包的 context key 碰撞（仿 outputLimitKey）。
type specLookupKey struct{}

// WithSpecLookup 把查询函数 f 附加到 ctx。
// f 为 nil 时原样返回 ctx，等价于未设置，SpecLookupFrom 将返回 nil。
func WithSpecLookup(ctx context.Context, f SpecLookup) context.Context {
	if f == nil {
		return ctx
	}
	return context.WithValue(ctx, specLookupKey{}, f)
}

// SpecLookupFrom 读取 ctx 中的查询函数；未设置或类型不符时返回 nil。
// 默认 ctx 下这是一次失败的类型断言，成本可忽略，Run 行为逐字节不变。
func SpecLookupFrom(ctx context.Context) SpecLookup {
	if lookup, ok := ctx.Value(specLookupKey{}).(SpecLookup); ok {
		return lookup
	}
	return nil
}

// NewSpeculativeRegistry 返回一个仅用于推测预跑的注册表。
//
// 它复用 NewWithLimit 完成内置工具注册与输出上限设置，但刻意不调用
// SetGuard —— 返回的注册表 guard == nil，checkGuard 成为空操作，因此
// 预跑不产生任何审计记录，也不发起任何审批请求。
//
// 绝不作为主注册表使用：主路径必须走 New/NewWithLimit 并显式安装安全链
// （策略 → 审批 → 审计）。把本注册表用于真实调用会绕过全部守卫，
// 造成审计缺失。
//
// yolo 固定为 false：推测只对 ClassifyEffect 判为 local-read 的非变更工具
// 有意义，非变更工具本就不受该标志影响；固定 false 可确保误传的变更工具
// 不会在预跑中真正改动工作区。
func NewSpeculativeRegistry(root string, maxOutput int) *Registry {
	return NewWithLimit(root, false, maxOutput)
}
