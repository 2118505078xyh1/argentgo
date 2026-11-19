package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// fakeCompactor records each call and returns a scripted shortening of the
// conversation. It never touches a session, keeping the agent test standalone.
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
	// Default: drop the oldest messages but keep the system prompt and the
	// tail, producing a much smaller estimate than the input.
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
		CompactThreshold: 1, // tiny: any realistic history trips it
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
	// The target window must be the 0.8 band (floored by minCompactWindow).
	for _, window := range compactor.windows {
		if window < minCompactWindow {
			t.Fatalf("window = %d, want >= %d", window, minCompactWindow)
		}
	}
}

func TestMidTurnCompactionUsesEightyPercentBand(t *testing.T) {
	const threshold = 50000
	// With a large threshold the derived window is exactly 0.8*T - reserve.
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
		// Land inside the band (0.8T < estimate < T): a second check must not
		// compact again just to shave a few more tokens.
		shorten: func(messages []llm.Message) []llm.Message {
			return []llm.Message{
				messages[0],
				{Role: "assistant", Content: strings.Repeat("x", 190000)}, // ~47.5k tokens
				{Role: "user", Content: "continue"},
			}
		},
	}
	s := &runState{
		compactor:        compactor,
		compactThreshold: threshold,
		messages: []llm.Message{
			{Role: "system", Content: "rules"},
			{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("x", 220000)}, // ~55k, over threshold
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
