package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// fakeCompactor 记录每次调用，并返回脚本化的对话缩短结果。它从不触碰会话，使 agent 测试保持独立。
// （接上行）
type fakeCompactor struct {
	calls    int
	windows  []int
	shorten  func([]llm.Message) []llm.Message
	noChange bool
}

func (f *fakeCompactor) MaybeCompact(messages []llm.Message, window int) ([]llm.Message, bool) {
	f.calls++
	f.windows = append(f.windows, window)
	if f.noChange {
		return messages, false
	}
	if f.shorten != nil {
		return f.shorten(messages), true
	}
	// 默认：丢弃最旧的消息但保留系统提示和尾部，产生比输入小得多的估算值。
	// （接上行）
	out := []llm.Message{{Role: "system", Content: messages[0].Content}}
	out = append(out, messages[len(messages)-2:]...)
	return out, true
}

func TestMidTurnCompactionRunsAfterToolBatch(t *testing.T) {
	compactor := &fakeCompactor{}
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return textReply("final"), nil
	}}
	var notices []string
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         4,
		CompactThreshold: 1, // 极小：任何真实历史都会触发它
		Compactor:        compactor,
		Emit: func(e Event) {
			if e.Kind == EventNotice && strings.Contains(e.Text, "compacted context") {
				notices = append(notices, e.Text)
			}
		},
	}}

	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text != "final" {
		t.Fatalf("text = %q", res.Text)
	}
	if compactor.calls == 0 {
		t.Fatal("mid-turn compactor was never called after the tool batch")
	}
	if len(notices) == 0 {
		t.Fatal("compaction did not emit an EventNotice")
	}
	// 目标窗口必须是 0.8 带（以 minCompactWindow 为下限）。
	for _, window := range compactor.windows {
		if window < minCompactWindow {
			t.Fatalf("window = %d, want >= %d", window, minCompactWindow)
		}
	}
}

func TestMidTurnCompactionUsesEightyPercentBand(t *testing.T) {
	const threshold = 50000
	// 阈值较大时，推导出的窗口恰好是 0.8*T - reserve。
	compactor := &fakeCompactor{noChange: true}
	tools := &fakeTools{
		specs: []llm.ToolSpec{toolSpec("read")},
		run: func(context.Context, string, string) (string, error) {
			return strings.Repeat("x", 220000), nil
		},
	}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n <= 2 {
			return toolReply(toolCall(fmt.Sprintf("c%d", n), "read", `{}`)), nil
		}
		return textReply("final"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         5,
		CompactThreshold: threshold,
		Compactor:        compactor,
	}}
	if _, err := runner.Run(context.Background(), "inspect"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if compactor.calls == 0 {
		t.Fatal("expected at least one compaction attempt")
	}
	want := int(float64(threshold)*compactionRecoveryBand) - summaryReserveTokens
	for _, window := range compactor.windows {
		if window != want {
			t.Fatalf("window = %d, want %d (0.8 band minus reserve)", window, want)
		}
	}
}

func TestMidTurnCompactionHysteresisReleasesPressure(t *testing.T) {
	const threshold = 50000
	compactor := &fakeCompactor{
		// 落在带内（0.8T < 估算值 < T）时，第二次检查不得为了再省几个 token 而重新压缩。
		// （接上行）
		shorten: func(messages []llm.Message) []llm.Message {
			return []llm.Message{
				messages[0],
				{Role: "assistant", Content: strings.Repeat("x", 190000)}, // 约 47.5k token
				{Role: "user", Content: "continue"},
			}
		},
	}
	s := &runState{
		compactor:        compactor,
		compactThreshold: threshold,
		messages: []llm.Message{
			{Role: "system", Content: "rules"},
			{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", 220000)}, // 约 55k，超过阈值
		},
	}
	s.maybeCompact()
	s.maybeCompact()
	if compactor.calls != 1 {
		t.Fatalf("compactor called %d times, want 1 (hysteresis band)", compactor.calls)
	}
	if got := EstimateMessages(s.messages); got > threshold {
		t.Fatalf("estimate after compaction = %d, want <= %d", got, threshold)
	}
}

func TestMidTurnCompactionNoopLeavesMessagesAlone(t *testing.T) {
	compactor := &fakeCompactor{noChange: true}
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if n == 1 {
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		return textReply("final"), nil
	}}
	runner := &Runner{Opts: Options{
		Model:            "m",
		Chatter:          chat,
		Tools:            tools,
		MaxTurns:         4,
		CompactThreshold: 1,
		Compactor:        compactor,
	}}
	res, err := runner.Run(context.Background(), "inspect")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Messages) < 4 {
		t.Fatalf("messages = %d, expected system/user/assistant/tool/assistant", len(res.Messages))
	}
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1", compactor.calls)
	}
}
