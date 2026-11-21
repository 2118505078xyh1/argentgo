package agent

import (
	"errors"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestShouldCompactRetryDecisionMatrix 覆盖完整决策矩阵：
// overflow 真/假 × retriesSoFar 0/1/>=2 × hasCompactor 真/假。
// 期望值按 MaxCompactRetries == 1 硬编码（由 TestMaxCompactRetriesIsOne 守护）。
func TestShouldCompactRetryDecisionMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		retriesSoFar int
		overflow     bool
		hasCompactor bool
		want         bool
	}{
		{"overflow/first/compactor", 0, true, true, true},
		{"overflow/first/no compactor", 0, true, false, false},
		{"overflow/exhausted/compactor", 1, true, true, false},
		{"overflow/exhausted/no compactor", 1, true, false, false},
		{"overflow/beyond/compactor", MaxCompactRetries + 1, true, true, false},
		{"overflow/beyond/no compactor", MaxCompactRetries + 1, true, false, false},
		{"non-overflow/first/compactor", 0, false, true, false},
		{"non-overflow/first/no compactor", 0, false, false, false},
		{"non-overflow/exhausted/compactor", 1, false, true, false},
		{"non-overflow/exhausted/no compactor", 1, false, false, false},
		{"non-overflow/beyond/compactor", MaxCompactRetries + 1, false, true, false},
		{"non-overflow/beyond/no compactor", MaxCompactRetries + 1, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ShouldCompactRetry(tc.retriesSoFar, tc.overflow, tc.hasCompactor); got != tc.want {
				t.Fatalf("ShouldCompactRetry(%d, %v, %v) = %v, want %v",
					tc.retriesSoFar, tc.overflow, tc.hasCompactor, got, tc.want)
			}
		})
	}
}

// TestShouldCompactRetryDoesNotMutateInputs 验证纯函数不改入参：
// 三个参数都是值类型，调用前后变量值必须保持不变。
func TestShouldCompactRetryDoesNotMutateInputs(t *testing.T) {
	t.Parallel()
	retries, overflow, compactor := 0, true, true
	_ = ShouldCompactRetry(retries, overflow, compactor)
	if retries != 0 || !overflow || !compactor {
		t.Fatalf("inputs mutated: retries = %d, overflow = %v, compactor = %v", retries, overflow, compactor)
	}
}

// TestShouldCompactRetryDeterministic 验证相同输入重复调用结果稳定，
// 且不因调用次序而改变；消耗预算（retriesSoFar 增加）才会从 true 变 false。
func TestShouldCompactRetryDeterministic(t *testing.T) {
	t.Parallel()
	want := ShouldCompactRetry(0, true, true)
	if !want {
		t.Fatal("first decision = false, want true")
	}
	for i := 0; i < 5; i++ {
		if got := ShouldCompactRetry(0, true, true); got != want {
			t.Fatalf("call %d = %v, want stable %v", i, got, want)
		}
	}
	if ShouldCompactRetry(1, true, true) {
		t.Fatal("exhausted budget must deterministically stop")
	}
}

// TestShouldCompactRetryCJKIrrelevant 验证判定向导与错误文本语言无关：
// 英文与中文的上下文溢出错误经 llm.ClassifyFailure 得到同样的 overflow 布尔值，
// 因而产生同样的重试判断；非溢出错误在两种语言下也都同样停止。
func TestShouldCompactRetryCJKIrrelevant(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{"english overflow", errors.New("provider returned 400 Bad Request: prompt is too long")},
		{"chinese overflow", errors.New("上下文窗口已用尽")},
		{"english auth", errors.New("provider returned 401 Unauthorized: invalid api key")},
		{"chinese auth", errors.New("认证失败，登录已过期")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			overflow := llm.ClassifyFailure(tc.err).Kind == llm.FailureContextOverflow
			got := ShouldCompactRetry(0, overflow, true)
			want := overflow // 首次重试、有压缩器时，结果只取决于是否溢出
			if got != want {
				t.Fatalf("%s: ShouldCompactRetry(0, %v, true) = %v, want %v", tc.name, overflow, got, want)
			}
		})
	}
}
