package security

import "context"

// 本文件提供"反馈重放审批器"原语：把一个固定的操作员更正意见包装成 Approver，
// 使任意 Chain.Check 在需要审批时都拿到带 *FeedbackRefusal 的拒绝，从而沿
// chain.go 的反馈转换路径产出可恢复的 *RefusalError。
//
// 它是纯内存的测试/接线辅助：默认不接入任何 Chain，也没有既有调用方；不修改
// chain.go 或任何既有 Approver 签名，只依赖标准库。

// feedbackReplayApprover 用一个固定反馈回答所有审批请求。
//
// Ask 恒返回 (false, *FeedbackRefusal{Feedback: feedback})：无论工具名、参数，
// 也无论 context 是否已取消。取消语义由 Chain.Check 在调用 Ask 之前处理
// （chain.go 中的 ctx.Err() 检查），此处不重复判断，以保证"恒拒绝"的不变量。
// 当 feedback 为空时，链会把空的更正意见当作单纯的"否"，转成终结性的 StopError。
type feedbackReplayApprover struct {
	feedback string
}

// 编译期断言：确保该类型满足 Approver 接口。
var _ Approver = (*feedbackReplayApprover)(nil)

// Ask 实现 Approver：始终拒绝并带上构造时给定的反馈。
func (a *feedbackReplayApprover) Ask(_ context.Context, _ string, _ string) (bool, error) {
	return false, &FeedbackRefusal{Feedback: a.feedback}
}

// NewFeedbackReplayApprover 构造一个重放固定反馈的审批器，供 app 层接线使用。
//
// 返回的 Approver 每次被询问都给出同样的更正意见，因此应只用于测试、回放或
// 明确希望以固定反馈拒绝所有审批的场景；不要把它当作交互式审批器。
func NewFeedbackReplayApprover(feedback string) Approver {
	return &feedbackReplayApprover{feedback: feedback}
}

// RefusalFromFeedback 把一段操作员反馈直接翻译成链能理解的 error。
//
// 它是 AnswerToError(FeedbackAnswer{Granted: false, Feedback: feedback}) 的便捷
// 包装，语义完全一致：feedback 非空时返回 *FeedbackRefusal（链会转成可恢复的
// *RefusalError），为空时返回 ErrRefused 哨兵（终结性的"否"）。纯函数、无状态，
// 便于测试与接线时无需手工构造 FeedbackAnswer。
func RefusalFromFeedback(feedback string) error {
	return AnswerToError(FeedbackAnswer{Granted: false, Feedback: feedback})
}
