package security

// 本文件提供“结构化拒绝原因”原语：把安全链与工具边界产生的错误归一成
// 带类别的结构化值，供调用方（例如 agent 循环）据此决策，而不必再从错误
// 文本里猜测拒绝原因。它只依赖标准库，不接入现有链，也不改动任何现有
// 错误类型或错误文本，当前零调用方。

import "errors"

// RefusalKind 是拒绝的结构化类别。取值稳定，可用于日志聚合与策略判断；
// 字面值不是面向用户的文案，调用方不应解析其拼写。
type RefusalKind string

const (
	// RefusalKindPolicyDeny 表示策略硬性禁止（ErrDenied / 未否决的 StopError）。
	RefusalKindPolicyDeny RefusalKind = "policy-deny"
	// RefusalKindApprovalDeclined 表示操作员明确否决（ErrRefused / Refused 的 StopError）。
	RefusalKindApprovalDeclined RefusalKind = "approval-declined"
	// RefusalKindSandboxDenied 表示沙箱拦截（只读文件系统、网络、权限等）。
	RefusalKindSandboxDenied RefusalKind = "sandbox-denied"
	// RefusalKindToolError 表示可恢复的工具错误（带反馈的否决、计划模式命令拦截）。
	RefusalKindToolError RefusalKind = "tool-error"
	// RefusalKindUnavailable 表示审批通道不可用，无人能够回答。
	RefusalKindUnavailable RefusalKind = "unavailable"
)

// RefusalReason 是一次拒绝的结构化描述。
// Kind 是稳定类别；Tool 是被拒工具名（无法从错误中结构化取得时为空）；
// Detail 是补充细节（StopError.Reason、RefusalError.Feedback 或错误文本）；
// Recoverable 表示模型可以调整请求后重试。
type RefusalReason struct {
	Kind        RefusalKind
	Tool        string
	Detail      string
	Recoverable bool
}

// ReasonFromError 把 err 映射成结构化拒绝原因。
//
// 匹配顺序（先带类型错误，后哨兵）：
//   - *RefusalError / *FeedbackRefusal → tool-error（可恢复）
//   - *StopError{Refused:true}       → approval-declined
//   - *StopError{Refused:false}      → sandbox-denied（Reason 命中沙箱违规分类器时）或 policy-deny
//   - ErrApprovalUnavailable         → unavailable
//   - ErrRecoverablePlanBlock        → tool-error（可恢复）
//   - ErrRefused                     → approval-declined
//   - ErrDenied                      → sandbox-denied 或 policy-deny
//
// err 为 nil 或没有任何已知错误匹配时返回 (零值, false)。它只读 err，
// 不会修改任何传入的错误对象。errors.As / errors.Is 保证包装链被穿透。
func ReasonFromError(err error) (RefusalReason, bool) {
	if err == nil {
		return RefusalReason{}, false
	}

	var refusal *RefusalError
	if errors.As(err, &refusal) {
		return RefusalReason{Kind: RefusalKindToolError, Tool: refusal.Tool,
			Detail: refusal.Feedback, Recoverable: true}, true
	}
	var feedback *FeedbackRefusal
	if errors.As(err, &feedback) {
		return RefusalReason{Kind: RefusalKindToolError,
			Detail: feedback.Feedback, Recoverable: true}, true
	}
	var stop *StopError
	if errors.As(err, &stop) {
		if stop.Refused {
			return RefusalReason{Kind: RefusalKindApprovalDeclined,
				Tool: stop.Tool, Detail: stop.Reason}, true
		}
		return refusalFromDenial(stop.Tool, stop.Reason), true
	}

	switch {
	case errors.Is(err, ErrApprovalUnavailable):
		return RefusalReason{Kind: RefusalKindUnavailable, Detail: err.Error()}, true
	case errors.Is(err, ErrRecoverablePlanBlock):
		return RefusalReason{Kind: RefusalKindToolError, Detail: err.Error(), Recoverable: true}, true
	case errors.Is(err, ErrRefused):
		return RefusalReason{Kind: RefusalKindApprovalDeclined, Detail: err.Error()}, true
	case errors.Is(err, ErrDenied):
		return refusalFromDenial("", err.Error()), true
	}
	return RefusalReason{}, false
}

// refusalFromDenial 区分硬性拒绝究竟来自沙箱还是策略。安全链没有为沙箱拦截
// 定义专门的 error 类型，但 StopError.Reason 会带上拦截原因，因此复用同包的
// 沙箱违规分类器做这一步判断；分类器不匹配时一律归为策略禁止（保守口径）。
func refusalFromDenial(tool, detail string) RefusalReason {
	kind := RefusalKindPolicyDeny
	if classified, _, ok := classifySandboxViolation(detail); ok && classified != "" {
		kind = RefusalKindSandboxDenied
	}
	return RefusalReason{Kind: kind, Tool: tool, Detail: detail}
}

// ModelHint 生成模型可读的自我修正提示。提示只由 Kind 与 Recoverable 决定，
// 因而是稳定的；Tool 与 Detail 作为结构化字段单独提供给调用方。
func (r RefusalReason) ModelHint() string {
	var hint string
	switch r.Kind {
	case RefusalKindPolicyDeny:
		hint = "该操作被策略禁止，请换一种方式。"
	case RefusalKindApprovalDeclined:
		hint = "操作员否决了该操作，请勿以相同方式重试。"
	case RefusalKindSandboxDenied:
		hint = "该操作被沙箱拦截，请改用沙箱允许的方式。"
	case RefusalKindToolError:
		hint = "该工具调用未获通过，请阅读反馈后调整。"
	case RefusalKindUnavailable:
		hint = "审批通道当前不可用，请稍后再试或改用无需审批的方式。"
	default:
		hint = "该操作未能完成，请换一种方式。"
	}
	if r.Recoverable {
		hint += "可以调整请求后重试。"
	}
	return hint
}
