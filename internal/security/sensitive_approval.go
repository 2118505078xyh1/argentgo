package security

import "context"

// 本文件提供"敏感审批"原语：给单次调用打上敏感标记，并让链只接受真人交互
// 审批器的批准。它复用既有 Approver 签名，通过 context 元数据与可选接口实现，
// 不触碰任何现有审批器的 Ask 方法（#23）。

// singleUseApprovalKey 标记"本次审批只认一次、不得持久化 always"的调用。
// context key 用私有类型，避免跨包复制导致取不到值。
type singleUseApprovalKey struct{}

// withSingleUseApproval 给 ctx 打上"不可持久化"标记。链在命中 #21 配置守卫时
// 调用它；持久化审批器据此把 always 降级为一次性放行。
func withSingleUseApproval(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, singleUseApprovalKey{}, true)
}

// singleUseFromContext 报告本次审批是否被标记为不可持久化。
func singleUseFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(singleUseApprovalKey{}).(bool)
	return marked
}

// InteractiveReplyApprover 是可选接口：实现它的审批器可以声明自己的回答是否
// 来自真人交互。链对敏感调用只接受 InteractiveReply() 为 true 的批准，机器
// /YOLO 客户端的自动批准无法代答。不实现该接口的审批器（例如 TUI/RPC 注入的
// 审批器）按可交互处理；自动审批器必须显式实现并返回 false 才会被识别为机器。
type InteractiveReplyApprover interface {
	InteractiveReply() bool
}

// approverAllowsInteractiveReply 报告 approver 的批准是否来自真人交互。
// 未知审批器默认视为可交互，避免把常见的 TUI/RPC 审批器误判为机器；
// nil 审批器没有回答，按不可交互处理。
func approverAllowsInteractiveReply(approver Approver) bool {
	if approver == nil {
		return false
	}
	if interactive, ok := approver.(InteractiveReplyApprover); ok {
		return interactive.InteractiveReply()
	}
	return true
}
