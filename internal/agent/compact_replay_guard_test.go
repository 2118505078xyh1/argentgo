package agent

import (
	"context"
	"errors"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestCompactReplayGuardDecisionMatrix 守护压缩重试的完整决策矩阵：
// overflow 真/假 × retriesSoFar 0/1/>=2 × hasCompactor 真/假。
// 期望值按 MaxCompactRetries == 1 硬编码（由现有的 TestMaxCompactRetriesIsOne 守护）：
// 只有首次溢出且配置了压缩器时才允许压缩并重放，其余组合一律停止。
// 使用包装函数 ShouldCompactRetry 的布尔签名，逐项断言。
func TestCompactReplayGuardDecisionMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		overflow     bool
		retriesSoFar int
		hasCompactor bool
		want         bool
	}{
		{"overflow/first/with-compactor", true, 0, true, true},
		{"overflow/first/no-compactor", true, 0, false, false},
		{"overflow/one-retry/with-compactor", true, 1, true, false},
		{"overflow/one-retry/no-compactor", true, 1, false, false},
		{"overflow/beyond-budget/with-compactor", true, MaxCompactRetries + 1, true, false},
		{"overflow/beyond-budget/no-compactor", true, MaxCompactRetries + 1, false, false},
		{"non-overflow/first/with-compactor", false, 0, true, false},
		{"non-overflow/first/no-compactor", false, 0, false, false},
		{"non-overflow/one-retry/with-compactor", false, 1, true, false},
		{"non-overflow/one-retry/no-compactor", false, 1, false, false},
		{"non-overflow/beyond-budget/with-compactor", false, MaxCompactRetries + 1, true, false},
		{"non-overflow/beyond-budget/no-compactor", false, MaxCompactRetries + 1, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ShouldCompactRetry(tc.retriesSoFar, tc.overflow, tc.hasCompactor)
			if got != tc.want {
				t.Fatalf("ShouldCompactRetry(%d, %v, %v) = %v, want %v",
					tc.retriesSoFar, tc.overflow, tc.hasCompactor, got, tc.want)
			}
		})
	}
}

// TestCompactReplayGuardMatchesDecideCompactRetry 确认包装层 ShouldCompactRetry 与核心纯决策
// DecideCompactRetry 对所有预算状态保持一致：overflow 映射到 FailureContextOverflow，
// 非 overflow 映射到代表性的 FailureAuth；有压缩器时两者结论必须相同，无压缩器时必须停止。
// 这防止接线层与 agentpolicy 的决策语义日后发生漂移。
func TestCompactReplayGuardMatchesDecideCompactRetry(t *testing.T) {
	t.Parallel()
	overflows := []bool{true, false}
	retries := []int{0, 1, MaxCompactRetries + 1, MaxCompactRetries + 5}
	for _, overflow := range overflows {
		kind := llm.FailureAuth
		if overflow {
			kind = llm.FailureContextOverflow
		}
		for _, soFar := range retries {
			coreReplay := DecideCompactRetry(kind, soFar, MaxCompactRetries) == CompactRetry
			if got := ShouldCompactRetry(soFar, overflow, true); got != coreReplay {
				t.Fatalf("overflow=%v soFar=%d: ShouldCompactRetry=%v, DecideCompactRetry replay=%v (mismatch)",
					overflow, soFar, got, coreReplay)
			}
			if got := ShouldCompactRetry(soFar, overflow, false); got {
				t.Fatalf("overflow=%v soFar=%d: no compactor must stop, got replay=true", overflow, soFar)
			}
		}
	}
}

// TestCompactReplayGuardInputsUnchanged 验证判定向导纯读输入：三个值类型参数在调用前后保持一致。
func TestCompactReplayGuardInputsUnchanged(t *testing.T) {
	t.Parallel()
	retries, overflow, hasCompactor := 0, true, true
	_ = ShouldCompactRetry(retries, overflow, hasCompactor)
	if retries != 0 || !overflow || !hasCompactor {
		t.Fatalf("inputs mutated: retries=%d overflow=%v hasCompactor=%v", retries, overflow, hasCompactor)
	}
}

// TestCompactReplayGuardDeterministic 验证相同输入重复调用结果稳定，且调用次序不影响结论；
// 只有预算被消耗（retriesSoFar 增加）才会从允许变为停止。
func TestCompactReplayGuardDeterministic(t *testing.T) {
	t.Parallel()
	first := ShouldCompactRetry(0, true, true)
	if !first {
		t.Fatal("first decision = false, want true")
	}
	for i := 0; i < 6; i++ {
		if got := ShouldCompactRetry(0, true, true); got != first {
			t.Fatalf("call %d = %v, want stable %v", i, got, first)
		}
		// 穿插不同输入，确保内部没有隐藏的可变状态。
		_ = ShouldCompactRetry(1, true, false)
	}
	if ShouldCompactRetry(1, true, true) {
		t.Fatal("exhausted budget must deterministically stop")
	}
}

// TestCompactReplayGuardCJKIndependent 验证判定与错误文本语言无关：
// 英文与中文的上下文溢出错误经 ClassifyFailure 得到同样的 overflow 布尔值，
// 从而产生同样的重试判断；非溢出错误在两种语言下也都同样停止。
func TestCompactReplayGuardCJKIndependent(t *testing.T) {
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
			if got != overflow {
				t.Fatalf("%s: ShouldCompactRetry(0, %v, true) = %v, want %v", tc.name, overflow, got, overflow)
			}
		})
	}
}

// TestCompactReplayGuardDisabledDefaultNoReplay 覆盖 opt-in 默认值：EnableCompactRetry 默认 false 时，
// 即使配置了压缩器且失败类别为上下文溢出，compactAndReplay 也必须直接返回 retried=false，
// 不调用压缩器、不发起重放请求、不消耗重试预算。这里直接调用 compactAndReplay 作为可测入口。
func TestCompactReplayGuardDisabledDefaultNoReplay(t *testing.T) {
	t.Parallel()
	chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
		return textReply("unexpected"), nil
	}}
	compactor := &fakeCompactor{}
	// 刻意不设置 EnableCompactRetry：零值即关闭。
	s := newRunState(Options{
		Model:     "m",
		Chatter:   chat,
		Tools:     &fakeTools{},
		Compactor: compactor,
	}, "hi", nil)

	before := s.compactRetries
	_, _, retried := s.compactAndReplay(context.Background(), 1,
		llm.FailureInfo{Kind: llm.FailureContextOverflow})
	if retried {
		t.Fatal("disabled default must not report a replay")
	}
	if compactor.calls != 0 {
		t.Fatalf("compactor calls = %d, want 0 when disabled", compactor.calls)
	}
	if chat.count() != 0 {
		t.Fatalf("model requests = %d, want 0 when disabled", chat.count())
	}
	if s.compactRetries != before {
		t.Fatalf("compactRetries = %d, want unchanged %d when disabled", s.compactRetries, before)
	}
}
