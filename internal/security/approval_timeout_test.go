package security

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestApprovalTimeoutBoundaries 覆盖 approvalTimeout 的所有边界：未配置回落
// 默认值、超大值钳制到上限，以及两个临界点原样保留。
func TestApprovalTimeoutBoundaries(t *testing.T) {
	const ns = time.Nanosecond
	tests := []struct {
		name     string
		provided time.Duration
		want     time.Duration
	}{
		{"zero falls back to default", 0, defaultApprovalTimeout},
		{"negative falls back to default", -1, defaultApprovalTimeout},
		{"large negative falls back to default", -time.Hour, defaultApprovalTimeout},
		{"minimum duration falls back to default", time.Duration(-1 << 63), defaultApprovalTimeout},
		{"one nanosecond is preserved", 1 * ns, 1 * ns},
		{"default value is preserved", defaultApprovalTimeout, defaultApprovalTimeout},
		{"just below maximum is preserved", maxApprovalTimeout - 1*ns, maxApprovalTimeout - 1*ns},
		{"exactly maximum is preserved", maxApprovalTimeout, maxApprovalTimeout},
		{"just above maximum is clamped", maxApprovalTimeout + 1*ns, maxApprovalTimeout},
		{"one day is clamped", 24 * time.Hour, maxApprovalTimeout},
		{"maximum duration is clamped", time.Duration(1<<63 - 1), maxApprovalTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approvalTimeout(tt.provided); got != tt.want {
				t.Fatalf("approvalTimeout(%v) = %v, want %v", tt.provided, got, tt.want)
			}
		})
	}
}

// TestApprovalTimeoutStableAndCJKIndependent 确认该函数是确定性的纯函数：
// 给定输入无论调用多少次、以何种顺序调用都得到相同结果，且结果始终落在
// [default, max] 区间内。函数只处理 time.Duration，不含任何字符串或 CJK
// 处理逻辑，因此结果天然与语言/字符编码无关。
func TestApprovalTimeoutStableAndCJKIndependent(t *testing.T) {
	inputs := []time.Duration{
		0,
		-time.Hour,
		defaultApprovalTimeout,
		maxApprovalTimeout,
		maxApprovalTimeout + time.Nanosecond,
		24 * time.Hour,
	}
	// 正向遍历一遍确定基线。
	baseline := make(map[time.Duration]time.Duration, len(inputs))
	for _, in := range inputs {
		baseline[in] = approvalTimeout(in)
	}
	// 逆序、重复打乱后再遍历，结果必须与基线一致。
	for i := len(inputs) - 1; i >= 0; i-- {
		in := inputs[i]
		for range 3 {
			if got := approvalTimeout(in); got != baseline[in] {
				t.Fatalf("approvalTimeout(%v) 不稳定：首次 %v，现在 %v", in, baseline[in], got)
			}
		}
	}
	for in, got := range baseline {
		if got < defaultApprovalTimeout || got > maxApprovalTimeout {
			t.Fatalf("approvalTimeout(%v) = %v 落在 [default, max] 之外", in, got)
		}
	}
}

// TestWithApprovalTimeoutAppliesDefault 确认接线函数把未配置的时长套用默认
// 超时，把超大时长钳制到上限。
func TestWithApprovalTimeoutAppliesDefault(t *testing.T) {
	const slack = 2 * time.Second
	tests := []struct {
		name     string
		provided time.Duration
		want     time.Duration
	}{
		{"unset uses default", 0, defaultApprovalTimeout},
		{"negative uses default", -1, defaultApprovalTimeout},
		{"oversized uses maximum", 24 * time.Hour, maxApprovalTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			ctx, cancel := withApprovalTimeout(context.Background(), tt.provided)
			defer cancel()
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("派生 context 缺少 deadline")
			}
			remaining := deadline.Sub(start)
			if remaining < tt.want-slack || remaining > tt.want+slack {
				t.Fatalf("剩余时长 = %v，期望约 %v（容差 %v）", remaining, tt.want, slack)
			}
		})
	}
}

// TestWithApprovalTimeoutDeadlineFailsClosedAsCancelled 确认超时后的审批
// 等待经由 ClassifyApproval 失败即拒绝地归为取消：即使底层审批器在超时的
// 同一刻返回 granted=true，也绝不能被规整为批准。
func TestWithApprovalTimeoutDeadlineFailsClosedAsCancelled(t *testing.T) {
	ctx, cancel := withApprovalTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("带超时的审批 context 未按时到期")
	}
	if !errorsIsDeadline(ctx.Err()) {
		t.Fatalf("ctx.Err() = %v，期望 DeadlineExceeded", ctx.Err())
	}
	for _, granted := range []bool{false, true} {
		if got := ClassifyApproval(granted, ctx.Err()); got != ApprovalCancelled {
			t.Fatalf("ClassifyApproval(%t, DeadlineExceeded) = %q，期望 %q", granted, got, ApprovalCancelled)
		}
	}
}

// errorsIsDeadline 是对 errors.Is(err, context.DeadlineExceeded) 的小封装，
// 让超时分类断言的意图更直白。
func errorsIsDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
