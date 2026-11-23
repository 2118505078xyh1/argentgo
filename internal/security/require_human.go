package security

import "context"

// 破窗规则（TODO #7）：某些调用的判定带有正交的 RequireHuman 标志，
// 表示"必须由真人当场决定"。它独立于 Decision：Allow + RequireHuman
// 仍要走审批，Deny + RequireHuman 仍是硬拒绝。
//
// 实现方式是给 ctx 打私有标记，让审批链与所有带缓存的审批器都看见它：
//   - Chain 遇到标记时强制敏感路径：忽略机器/YOLO 自动批准；
//   - CachedApprover / ScopedApprover / FileBackedScopedApprover 跳过
//     always/limited 缓存，且不再持久化新授予；
//   - 没有真人交互通道时直接拒绝，而不是等待一个不会到来的回答。

// requireHumanKey 是 RequireHuman 标记的私有 context key。
type requireHumanKey struct{}

// WithRequireHuman 把"必须真人审批"标记注入 ctx。标记只影响本次调用链，
// 不会修改任何策略、审批器或全局状态。
func WithRequireHuman(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requireHumanKey{}, true)
}

// RequireHumanFrom 报告 ctx 是否携带 RequireHuman 标记。
func RequireHumanFrom(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(requireHumanKey{}).(bool)
	return marked
}
