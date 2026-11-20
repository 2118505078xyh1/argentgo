package agent

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestCompactRetryDecisionWireValues 锁定稳定的字符串值，使前端或持久化的记录无需导入本包的
// 类型即可比较它们。
// （接上行）
func TestCompactRetryDecisionWireValues(t *testing.T) {
	t.Parallel()
	if string(CompactRetry) != "retry" {
		t.Fatalf("CompactRetry = %q, want %q", CompactRetry, "retry")
	}
	if string(CompactStop) != "stop" {
		t.Fatalf("CompactStop = %q, want %q", CompactStop, "stop")
	}
}

// TestDecideCompactRetryOnlyOverflowRetries 覆盖封闭的失败词汇表：只有上下文溢出值得压缩重试，
// 其他所有类别都保持原有的停止行为。
// （接上行）
func TestDecideCompactRetryOnlyOverflowRetries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind llm.FailureKind
		want CompactRetryDecision
	}{
		{"context overflow", llm.FailureContextOverflow, CompactRetry},
		{"auth", llm.FailureAuth, CompactStop},
		{"quota", llm.FailureQuota, CompactStop},
		{"rate limit", llm.FailureRateLimit, CompactStop},
		{"config", llm.FailureConfig, CompactStop},
		{"transient", llm.FailureTransient, CompactStop},
		{"agent action", llm.FailureAgentAction, CompactStop},
		{"internal", llm.FailureInternal, CompactStop},
		{"unknown", llm.FailureUnknown, CompactStop},
		{"zero value", llm.FailureKind(""), CompactStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DecideCompactRetry(tc.kind, 0, MaxCompactRetries); got != tc.want {
				t.Fatalf("DecideCompactRetry(%q, 0, %d) = %q, want %q", tc.kind, MaxCompactRetries, got, tc.want)
			}
		})
	}
}

// TestDecideCompactRetryBudgetBoundaries 检查重试计数器和 maxRetries 上限，包括「maxRetries <= 0 即零」规则。
// （接上行）
func TestDecideCompactRetryBudgetBoundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		kind         llm.FailureKind
		retriesSoFar int
		maxRetries   int
		want         CompactRetryDecision
	}{
		{"first retry with budget one", llm.FailureContextOverflow, 0, 1, CompactRetry},
		{"budget one exhausted", llm.FailureContextOverflow, 1, 1, CompactStop},
		{"budget one exceeded", llm.FailureContextOverflow, 2, 1, CompactStop},
		{"budget two first", llm.FailureContextOverflow, 0, 2, CompactRetry},
		{"budget two second", llm.FailureContextOverflow, 1, 2, CompactRetry},
		{"budget two exhausted", llm.FailureContextOverflow, 2, 2, CompactStop},
		{"zero max", llm.FailureContextOverflow, 0, 0, CompactStop},
		{"negative max", llm.FailureContextOverflow, 0, -1, CompactStop},
		{"negative max and negative retries", llm.FailureContextOverflow, -3, -1, CompactStop},
		{"other kind with budget", llm.FailureTransient, 0, 1, CompactStop},
		{"other kind with large budget", llm.FailureAuth, 0, 10, CompactStop},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DecideCompactRetry(tc.kind, tc.retriesSoFar, tc.maxRetries); got != tc.want {
				t.Fatalf("DecideCompactRetry(%q, %d, %d) = %q, want %q", tc.kind, tc.retriesSoFar, tc.maxRetries, got, tc.want)
			}
		})
	}
}

// TestDecideCompactRetryIsStable 证明函数是纯的：相同输入的重复调用不会交替结果，而消耗预算会把重试变成永久停止。
// （接上行）
// （接上行）
func TestDecideCompactRetryIsStable(t *testing.T) {
	t.Parallel()
	first := DecideCompactRetry(llm.FailureContextOverflow, 0, MaxCompactRetries)
	if first != CompactRetry {
		t.Fatalf("first decision = %q, want %q", first, CompactRetry)
	}
	for i := 0; i < 5; i++ {
		if got := DecideCompactRetry(llm.FailureContextOverflow, 0, MaxCompactRetries); got != first {
			t.Fatalf("call %d = %q, want stable %q", i, got, first)
		}
	}

	after := DecideCompactRetry(llm.FailureContextOverflow, 1, MaxCompactRetries)
	if after != CompactStop {
		t.Fatalf("post-retry decision = %q, want %q", after, CompactStop)
	}
	for i := 0; i < 5; i++ {
		if got := DecideCompactRetry(llm.FailureContextOverflow, 1, MaxCompactRetries); got != after {
			t.Fatalf("call %d after retry = %q, want stable %q", i, got, after)
		}
	}
}

// TestMaxCompactRetriesIsOne 守护循环所依赖的仅一次重试的上限。
func TestMaxCompactRetriesIsOne(t *testing.T) {
	t.Parallel()
	if MaxCompactRetries != 1 {
		t.Fatalf("MaxCompactRetries = %d, want 1", MaxCompactRetries)
	}
}
