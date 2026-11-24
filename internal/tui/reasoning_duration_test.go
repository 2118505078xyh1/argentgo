package tui

import (
	"strings"
	"testing"
	"time"
)

// TestReasoningDurationFromEvent 验证桥接传入的真实推理时长会落到条目的
// thinkingAt/thinkingEnd，折叠摘要显示非 0 时长（修复"思考时间 0ms"）。
func TestReasoningDurationFromEvent(t *testing.T) {
	m := New(Config{Model: "m"})
	m.HandleEvent(Event{Kind: EventReasoning, Reasoning: "让我想想", ReasoningDurationMS: 2400})
	got := m.Transcript()
	if len(got) != 1 || got[0].Kind != EntryReasoning {
		t.Fatalf("entries = %+v", got)
	}
	e := got[0]
	if e.thinkingAt.IsZero() || e.thinkingEnd.IsZero() {
		t.Fatal("reasoning timing not set from event duration")
	}
	if d := e.thinkingEnd.Sub(e.thinkingAt); d != 2400*time.Millisecond {
		t.Fatalf("reasoning duration = %v, want 2.4s", d)
	}
	line := reasoningSummaryLine(e.Text, e.thinkingEnd.Sub(e.thinkingAt), 80, true, "zh", New(Config{}).palette)
	if !strings.Contains(line.Text(), "2.4s") {
		t.Fatalf("summary = %q, want 2.4s", line.Text())
	}
}

// TestReasoningWithoutDurationFallsBack 无时长事件维持旧行为（首帧计时），
// 不产生"负时长"或异常。
func TestReasoningWithoutDurationFallsBack(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{Kind: EventReasoning, Reasoning: "x"})
	if len(m.Transcript()) != 1 {
		t.Fatal("reasoning entry missing")
	}
}
