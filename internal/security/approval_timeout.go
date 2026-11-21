package security

import (
	"context"
	"time"
)

const (
	// defaultApprovalTimeout 是审批等待的默认超时。当调用方传入 0 或负数
	// （即"未配置"）时套用它，防止无人应答时无限期挂起。
	defaultApprovalTimeout = 5 * time.Minute
	// maxApprovalTimeout 是审批等待的上限。超过它的配置会被钳制到该值，
	// 避免把 operator 的等待时间设成实质上的无限期。
	maxApprovalTimeout = 30 * time.Minute
)

// approvalTimeout 把调用方提供的审批等待时长规整到一个安全区间。
//
// 规整规则（纯函数，无状态、无副作用，可安全并发使用）：
//   - provided <= 0（未配置或非法值）返回 defaultApprovalTimeout；
//   - provided > maxApprovalTimeout 返回 maxApprovalTimeout；
//   - 其余情况原样返回 provided。
//
// 边界是闭区间且稳定的：默认值、上限这两个临界点会原样保留，只有严格
// 大于上限的值才被钳制。该函数只处理 time.Duration 数值，不涉及任何
// 字符串、区域设置或 CJK 字符，因此结果与语言/字符编码无关。
func approvalTimeout(provided time.Duration) time.Duration {
	if provided <= 0 {
		return defaultApprovalTimeout
	}
	if provided > maxApprovalTimeout {
		return maxApprovalTimeout
	}
	return provided
}

// withApprovalTimeout 在 parent 上派生一个带审批超时的 context。
// 它把 approvalTimeout 的规整结果交给 context.WithTimeout，因此默认的
// fail-closed 语义由调用链下游的 ClassifyApproval 保证：一旦超时，
// DeadlineExceeded 会被归为 ApprovalCancelled（取消），绝不会被当成批准。
//
// 调用方在等待结束后应调用返回的 cancel 以释放计时器资源。
func withApprovalTimeout(parent context.Context, provided time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, approvalTimeout(provided))
}
