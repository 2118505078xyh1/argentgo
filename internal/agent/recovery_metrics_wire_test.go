package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// runRecoveryMetricsScenario 驱动一段"工具失败→（可选）成功→文本收尾"的脚本运行。
// 返回 runState（读取恢复账本）与最终消息流（比对开关是否改变发给模型的字节）。
// 每次只发出一个工具调用，因此调用序号与轮次号一一对应。
func runRecoveryMetricsScenario(t *testing.T, enabled bool, failRounds map[int]bool, toolRounds int) (*runState, []llm.Message) {
	t.Helper()
	if enabled {
		t.Setenv("ARGENT_RECOVERY_METRICS", "")
	} else {
		t.Setenv("ARGENT_RECOVERY_METRICS", "0")
	}

	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n <= toolRounds {
			return toolReply(toolCall(fmt.Sprintf("c%d", n), "bad", fmt.Sprintf(`{"n":%d}`, n))), nil
		}
		return textReply("done"), nil
	}}

	var mu sync.Mutex
	callNo := 0
	ft := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("bad")},
		run: func(context.Context, string, string) (string, error) {
			mu.Lock()
			callNo++
			n := callNo
			mu.Unlock()
			if failRounds[n] {
				return "", errors.New("boom")
			}
			return "ok", nil
		},
	}

	s := newRunState(Options{
		Model:      "test",
		Chatter:    chat,
		Tools:      ft,
		MaxTurns:   16,
		MaxRepeats: 100, // 抬高重复防护额度，使脚本化失败不被防护拦截（拦截的调用 ran=false，不会推进错误序列）。
	}, "go", nil)
	// limit=1：第一次失败后每轮 Check 都给出指引动作，便于精确统计注入次数与恢复记账。
	s.mistakes = NewMistakeTracker(1, func(MistakeKind, int) string { return "RECOVERY" })

	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	return s, res.Messages
}

// TestRecoveryMetricsWireAttemptSuccessAndRate 验证默认开启时，
// 指引注入登记一次 attempt、随后的工具成功登记一次 success，成功率为 1。
func TestRecoveryMetricsWireAttemptSuccessAndRate(t *testing.T) {
	// 第 1 次工具失败触发指引注入（attempt），第 2 次成功清零连续序列（success）。
	s, _ := runRecoveryMetricsScenario(t, true, map[int]bool{1: true}, 2)

	if s.recovery == nil {
		t.Fatal("默认开启时 recovery 账本应为非 nil")
	}
	snap := s.recoverySnapshot()
	want := RecoveryStats{Attempts: 1, Successes: 1, Rate: 1, Outcome: RecoveryOutcomeRecovered}
	got := snap[string(MistakeKindToolExecutionFailed)]
	if got != want {
		t.Fatalf("recoverySnapshot()[tool_execution_failed] = %+v, want %+v（全部: %+v）", got, want, snap)
	}
}

// TestRecoveryMetricsWirePersistedWithoutSuccess 验证一直失败、从未清零时只累计 attempt，
// 成功数为零、成功率为零、最近结果为 persisted。
func TestRecoveryMetricsWirePersistedWithoutSuccess(t *testing.T) {
	s, _ := runRecoveryMetricsScenario(t, true, map[int]bool{1: true, 2: true}, 2)
	got := s.recoverySnapshot()[string(MistakeKindToolExecutionFailed)]
	want := RecoveryStats{Attempts: 2, Successes: 0, Rate: 0, Outcome: RecoveryOutcomePersisted}
	if got != want {
		t.Fatalf("持续失败时恢复账本 = %+v, want %+v", got, want)
	}
}

// TestRecoveryMetricsWireNoAttemptNoSuccess 验证成功信号受"先有 attempt"门控：
// 工具成功但从未触发指引时，不应凭空登记 success。
func TestRecoveryMetricsWireNoAttemptNoSuccess(t *testing.T) {
	s, _ := runRecoveryMetricsScenario(t, true, nil, 1)
	if snap := s.recoverySnapshot(); len(snap) != 0 {
		t.Fatalf("无尝试时不应登记恢复数据, 得到 %+v", snap)
	}
}

// TestRecoveryMetricsWireOffBaselineByteIdentical 锁定 =0 关闭时的基线：
// 账本为 nil、快照为空，且与默认开启时相比发给模型的消息流逐字节不变。
func TestRecoveryMetricsWireOffBaselineByteIdentical(t *testing.T) {
	fail := map[int]bool{1: true}
	sOff, msgsOff := runRecoveryMetricsScenario(t, false, fail, 2)
	if sOff.recovery != nil {
		t.Fatal("=0 关闭时 recovery 账本应为 nil")
	}
	if snap := sOff.recoverySnapshot(); snap == nil || len(snap) != 0 {
		t.Fatalf("=0 关闭时快照应为非 nil 空 map, 得到 %+v", snap)
	}

	sOn, msgsOn := runRecoveryMetricsScenario(t, true, fail, 2)
	if len(sOn.recoverySnapshot()) == 0 {
		t.Fatal("默认开启时应记录到恢复数据")
	}
	if !reflect.DeepEqual(msgsOn, msgsOff) {
		t.Fatalf("开关改变了消息流:\n on =%+v\n off=%+v", msgsOn, msgsOff)
	}
}

// TestRecoveryMetricsWireMultipleKinds 验证多种错误种类各自独立累计，
// 待恢复种类在成功信号后被清空，下一种类从干净状态开始。
func TestRecoveryMetricsWireMultipleKinds(t *testing.T) {
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	s := newRunState(Options{}, "go", nil)

	s.recordRecoveryAttempt("api_error")
	s.recordRecoveryAttempt("api_error")
	s.recordRecoverySuccess()                    // api_error: 2 attempts / 1 success
	s.recordRecoveryAttempt("invalid_tool_call") // 未成功 → persisted
	s.recordRecoveryAttempt("tool_execution_failed")
	s.recordRecoverySuccess() // tool_execution_failed: 1 attempt / 1 success

	snap := s.recoverySnapshot()
	want := map[string]RecoveryStats{
		"api_error":             {Attempts: 2, Successes: 1, Rate: 0.5, Outcome: RecoveryOutcomeRecovered},
		"invalid_tool_call":     {Attempts: 1, Successes: 0, Rate: 0, Outcome: RecoveryOutcomePersisted},
		"tool_execution_failed": {Attempts: 1, Successes: 1, Rate: 1, Outcome: RecoveryOutcomeRecovered},
	}
	if !reflect.DeepEqual(snap, want) {
		t.Fatalf("多 kind 快照 = %+v, want %+v", snap, want)
	}
}

// TestRecoveryMetricsWireCJK 验证 CJK 种类可作为账本键，且待恢复归属与结果转换正常：
// 首次成功后再失败一次，成功数保留、最近结果转为 persisted。
func TestRecoveryMetricsWireCJK(t *testing.T) {
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	s := newRunState(Options{}, "go", nil)

	const kind = "工具执行失败：权限不足"
	s.recordRecoveryAttempt(kind)
	s.recordRecoverySuccess()
	s.recordRecoveryAttempt(kind) // 第二次未成功

	got := s.recoverySnapshot()[kind]
	want := RecoveryStats{Attempts: 2, Successes: 1, Rate: 0.5, Outcome: RecoveryOutcomePersisted}
	if got != want {
		t.Fatalf("CJK kind 快照 = %+v, want %+v", got, want)
	}
}

// TestRecoveryMetricsWireCoexistWithRepeatPitfall 验证恢复账本与跨情节重复坑计数
// 两套默认开启的开关可同时开启、各自独立累计，互不干扰。
func TestRecoveryMetricsWireCoexistWithRepeatPitfall(t *testing.T) {
	t.Setenv("ARGENT_RECOVERY_METRICS", "1")
	orig := repeatPitfallTrackingEnabled()
	SetRepeatPitfallTracking(true)
	t.Cleanup(func() { SetRepeatPitfallTracking(orig) })

	s := newRunState(Options{}, "go", nil)
	// 重复坑记在 MistakeTracker，恢复成效记在账本。
	s.mistakes.RecordSignature("连接超时：重试 3 次")
	s.mistakes.RecordSignature("连接超时：重试 4 次")
	s.recordRecoveryAttempt("api_error")
	s.recordRecoverySuccess()

	pitfalls := s.mistakes.Snapshot(10)
	if len(pitfalls) != 1 || pitfalls[0].Count != 2 {
		t.Fatalf("重复坑计数 = %+v, want 单个签名次数 2", pitfalls)
	}
	rec := s.recoverySnapshot()["api_error"]
	want := RecoveryStats{Attempts: 1, Successes: 1, Rate: 1, Outcome: RecoveryOutcomeRecovered}
	if rec != want {
		t.Fatalf("恢复账本 = %+v, want %+v", rec, want)
	}
}
