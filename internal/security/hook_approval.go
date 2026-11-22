package security

import "context"

// 本文件提供钩子预审批的进程内打章通道。它不引入新的 Approver 签名，也不改变
// 默认审批流程：只有 app 层在 PreToolUse 返回 allow 裁决时显式打章，Chain.check
// 才在询问审批器之前查章并跳过弹窗。策略与审计照常执行。

type hookApprovalKey struct{}

type hookApprovalStamp struct {
	tool string
	args string
}

// WithHookApproval 返回一个携带「该工具调用已被钩子预审批」标记的 context。
// 标记按工具名与参数字节绑定，避免一次 allow 裁决被移用到另一次调用上。
func WithHookApproval(ctx context.Context, tool, args string) context.Context {
	return context.WithValue(ctx, hookApprovalKey{}, hookApprovalStamp{tool: tool, args: args})
}

// HookApproved 报告 ctx 是否携带与给定工具名和参数匹配的预审批标记。
// 没有标记时恒返回 false，默认审批路径逐字节不变。
func HookApproved(ctx context.Context, tool, args string) bool {
	if ctx == nil {
		return false
	}
	stamp, ok := ctx.Value(hookApprovalKey{}).(hookApprovalStamp)
	return ok && stamp.tool == tool && stamp.args == args
}
