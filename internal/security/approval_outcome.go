package security

import (
	"context"
	"errors"
)

// ApprovalOutcome 是对 Approver.Ask 调用结果分类后的值。
//
// Approver.Ask 只返回裸的 (bool, error)。一个简单的 bool 会把四种截然不同的
// 情形压缩成两个值：真正的"是"、真正的"否"、损坏或缺失的审批通道，
// 以及被取消/超时的询问。无头运行和子代理正是人工审批通道可能缺席的场景，
// 因此它们必须能区分"操作员已批准"和"无人应答"。
// 这个四值词汇表在不改变 Approver 接口（全仓库都在使用）的前提下，
// 为它们提供了这种区分能力。
// ApprovalAllowed 是真正的肯定回答：无错误地授予。
type ApprovalOutcome string

const (
	// 它是唯一允许工具调用的结果。
	// ApprovalRejected 是明确的否定回答，且无错误。
	ApprovalAllowed ApprovalOutcome = "allowed"
	// ApprovalUnavailable 表示审批通道无法给出
	ApprovalRejected ApprovalOutcome = "rejected"
	// 答案（任何非 context 的错误）。失败即拒绝：绝不允许。
	// ApprovalCancelled 表示询问被取消或超时。失败即拒绝。
	ApprovalUnavailable ApprovalOutcome = "unavailable"
	// Allows 报告该结果是否是真正的批准。它是唯一的
	ApprovalCancelled ApprovalOutcome = "cancelled"
)

// 失败即拒绝闸门：只有 ApprovalAllowed 返回 true，因此不可用或
// 已取消的审批通道永远不能被规整为同意。
// ClassifyApproval 把 Approver.Ask 返回的原始 (granted, err) 对
func (o ApprovalOutcome) Allows() bool { return o == ApprovalAllowed }

// 规整为一个 ApprovalOutcome。
// 该分类刻意采用失败即拒绝：
//
//   - 非 nil 错误绝不代表允许，即使 granted 为 true；
//   - context.Canceled / context.DeadlineExceeded（包括通过 errors.Is
//     包装过的那些）变为 ApprovalCancelled；
//   - 任何其他错误变为 ApprovalUnavailable；
//   - 只有 (true, nil) 是 ApprovalAllowed，(false, nil) 是 ApprovalRejected。
//
// 它是纯函数：无状态、无副作用，可安全并发使用。
//
// Approver 询问人类是否允许某次工具调用运行。
func ClassifyApproval(granted bool, err error) ApprovalOutcome {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ApprovalCancelled
		}
		return ApprovalUnavailable
	}
	if granted {
		return ApprovalAllowed
	}
	return ApprovalRejected
}
