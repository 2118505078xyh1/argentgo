package security

import "errors"

// 本文件提供"审批回答 → 错误"的适配原语，让 app 层能把 Approver.Ask 的多态
// 返回值（bool + error，其中错误可能是带类型的 FeedbackRefusal）归一成一个
// FeedbackAnswer，再把它翻译成链能理解的 error。全部为纯函数：无状态、无副作用、
// 可安全并发使用，且只依赖标准库。默认没有任何调用方，也不会修改 chain.go 或
// 任何既有 Approver 签名。

// FeedbackAnswer 是一次审批回答的规范化表示：是否放行 + 可选的更正意见。
//
// 它把 Approver.Ask 的 (bool, error) 二元组压缩成一个值，使调用方无需关心
// 反馈是通过 bool 还是通过带类型的错误传回来的。Granted 与 Feedback 的约束：
// 只有 Granted=false 时 Feedback 才有意义；Granted=true 时的 Feedback 一律被忽略。
type FeedbackAnswer struct {
	Granted  bool
	Feedback string
}

// AnswerToError 把规范化的审批回答翻译成链能理解的 error。
//
// 映射规则：
//   - Granted=true → nil。此时即使 Feedback 非空也一律丢弃：同意就是同意，
//     绝不把反馈附加到批准结果上（批准路径没有可供模型读取的反馈错误）。
//   - Granted=false 且 Feedback 非空 → *FeedbackRefusal。链会在 Check 中把它
//     转成模型可读、可据此行动的 *RefusalError（可恢复的工具错误），而不是
//     终结性的 StopError。Feedback 原样透传，由链负责裁剪与 trim。
//   - Granted=false 且 Feedback 为空 → 普通 ErrRefused 哨兵错误。它映射到
//     StopError 的终结语义：操作员说了"否"，模型不应重试。
//
// 它是纯函数，不修改任何状态，也不触碰审计或策略。
func AnswerToError(ans FeedbackAnswer) error {
	if ans.Granted {
		// 放行时绝不携带反馈：批准结果没有承载反馈的通道，静默丢弃即可。
		return nil
	}
	if ans.Feedback != "" {
		return &FeedbackRefusal{Feedback: ans.Feedback}
	}
	return ErrRefused
}

// ParseApproverResult 把 Approver.Ask 返回的多态 (granted, err) 归一成 FeedbackAnswer。
//
// 当 err（或其包装链）中存在 *FeedbackRefusal 时，反馈是操作员的更正意见，
// 优先于裸的 granted 值：提取其 Feedback 并视为拒绝（Granted=false）。这是失败即
// 拒绝的一致性要求——带反馈的拒绝永远不能被误读成同意。其他任何错误（基础设施
// 故障、取消、超时等）都不携带反馈，退化为按 granted 构造 Answer。是否把这类错误
// 进一步分类为不可用/取消，由 ClassifyApproval 负责，不在本函数的职责范围内。
//
// 用 errors.As 匹配包装过的错误，与 chain.go 识别反馈拒绝的方式保持一致。
func ParseApproverResult(granted bool, err error) FeedbackAnswer {
	var refusal *FeedbackRefusal
	if errors.As(err, &refusal) {
		return FeedbackAnswer{Granted: false, Feedback: refusal.Feedback}
	}
	return FeedbackAnswer{Granted: granted}
}
