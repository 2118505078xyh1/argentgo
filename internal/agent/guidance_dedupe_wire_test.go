package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"example.com/argentgo/internal/llm"
)

// runGuidanceDedupeScenario 驱动一段"连续失败→（可选）成功→再次失败"的脚本运行。
// failRounds 以 1 起的调用序号标记哪些工具调用返回错误；toolRounds 是最后一条
// 携带工具调用的助手轮次，之后模型返回纯文本答案让循环干净结束。
// 每次只发出一个工具调用，因此调用序号与轮次号一一对应。
func runGuidanceDedupeScenario(t *testing.T, dedupeOn bool, failRounds map[int]bool, toolRounds int) []llm.Message {
	t.Helper()
	SetGuidanceDedupe(dedupeOn)
	t.Cleanup(func() { SetGuidanceDedupe(false) })

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
		MaxRepeats: 100, // 抬高重复防护额度，使被脚本化的失败调用不被防护拦截（拦截的调用 ran=false，不会推进错误序列）。
	}, "go", nil)
	// limit=1：第一次失败后每轮 Check 都会给出指引动作，便于精确统计注入条数。
	s.mistakes = NewMistakeTracker(1, func(MistakeKind, int) string { return "RECOVERY" })

	res, err := s.loop(context.Background())
	if err != nil {
		t.Fatalf("loop() error: %v", err)
	}
	return res.Messages
}

// countGuidance 统计消息流中以给定文本注入的用户轮次条数。
func countGuidance(msgs []llm.Message, text string) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && m.Content == text {
			n++
		}
	}
	return n
}

// TestGuidanceDedupeInjectsOnceAcrossConsecutiveFailures 验证 opt-in 开启时，
// 五次连续失败只注入一条恢复指引；同一断开的签名只注入一次。
func TestGuidanceDedupeInjectsOnceAcrossConsecutiveFailures(t *testing.T) {
	fail := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}
	msgs := runGuidanceDedupeScenario(t, true, fail, 6)
	if got := countGuidance(msgs, "RECOVERY"); got != 1 {
		t.Fatalf("dedupe on: injected %d guidance(s), want 1", got)
	}
}

// TestGuidanceDedupeOffKeepsLegacyPerRoundInjection 验证 opt-in 关闭（默认）时行为逐字节不变：
// 同一批失败在每个错误上限轮次都会照常注入指引（与去重开启形成对照）。
func TestGuidanceDedupeOffKeepsLegacyPerRoundInjection(t *testing.T) {
	fail := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}
	msgs := runGuidanceDedupeScenario(t, false, fail, 6)
	if got := countGuidance(msgs, "RECOVERY"); got != 5 {
		t.Fatalf("dedupe off: injected %d guidance(s), want 5 (legacy per-round)", got)
	}
}

// TestGuidanceDedupeResetAllowsReinjectAfterSuccess 验证在一次工具成功清空连续序列后，
// 新一轮失败可以再次注入同一签名的指引——即 loop 侧成功轨迹触发的 Reset 生效。
func TestGuidanceDedupeResetAllowsReinjectAfterSuccess(t *testing.T) {
	// 第 5 次调用成功（清空序列），第 6、7 次再次失败。
	fail := map[int]bool{1: true, 2: true, 3: true, 4: true, 6: true, 7: true}
	msgs := runGuidanceDedupeScenario(t, true, fail, 7)
	if got := countGuidance(msgs, "RECOVERY"); got != 2 {
		t.Fatalf("dedupe on after success: injected %d guidance(s), want 2", got)
	}
}
