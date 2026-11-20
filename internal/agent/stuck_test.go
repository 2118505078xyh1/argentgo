package agent

import (
	"fmt"
	"testing"
)

// stuckToolPair 为一次调用返回 tool_start/tool_end 对。id 不同，因此检测器必须以 name+args+isError 为键，
// 而非易变的 id。
func stuckToolPair(id, name, args string, isError bool) []Event {
	return []Event{
		{Kind: EventToolStart, ToolCallID: id, ToolName: name, Args: args},
		{Kind: EventToolEnd, ToolCallID: id, ToolName: name, IsError: isError},
	}
}

func stuckRepeatedCalls(n int) []Event {
	var out []Event
	for i := 0; i < n; i++ {
		out = append(out, stuckToolPair(fmt.Sprintf("call-%d", i), "read_file", `{"path":"a.go"}`, false)...)
	}
	return out
}

func TestStuckDetectorRepeatedCallTriggers(t *testing.T) {
	d := &StuckDetector{}
	guidance, stuck := d.Observe(stuckRepeatedCalls(stuckRepeatCalls))
	if !stuck {
		t.Fatal("three identical tool calls should be detected as stuck")
	}
	if guidance != stuckGuidanceRepeat {
		t.Fatalf("guidance = %q, want %q", guidance, stuckGuidanceRepeat)
	}

	// 两次重复低于阈值。
	d.Reset()
	guidance, stuck = d.Observe(stuckRepeatedCalls(stuckRepeatCalls - 1))
	if stuck || guidance != "" {
		t.Fatalf("two repeats: got stuck=%v guidance=%q, want not stuck", stuck, guidance)
	}
}

func TestStuckDetectorNormalizesArgumentKeyOrder(t *testing.T) {
	// 同一 JSON 对象的三种写法：键顺序和空白不同，但规范化后是一次重复的调用。
	// （接上行）
	spellings := []string{
		`{"path":"a","line":1}`,
		`{"line":1,"path":"a"}`,
		`{ "path" : "a", "line" : 1 }`,
	}

	t.Run("reordered keys are one repeated call", func(t *testing.T) {
		var events []Event
		for i, args := range spellings {
			events = append(events, stuckToolPair(fmt.Sprintf("call-%d", i), "read_file", args, false)...)
		}
		d := &StuckDetector{}
		guidance, stuck := d.Observe(events)
		if !stuck || guidance != stuckGuidanceRepeat {
			t.Fatalf("reordered keys: got stuck=%v guidance=%q, want repeat nudge", stuck, guidance)
		}
	})

	t.Run("different values are not merged", func(t *testing.T) {
		// 规范化不得把参数值不同的调用合并。
		events := []Event{}
		events = append(events, stuckToolPair("1", "read_file", spellings[0], false)...)
		events = append(events, stuckToolPair("2", "read_file", `{"line":2,"path":"a"}`, false)...)
		events = append(events, stuckToolPair("3", "read_file", spellings[0], false)...)
		d := &StuckDetector{}
		guidance, stuck := d.Observe(events)
		if stuck || guidance != "" {
			t.Fatalf("distinct argument values: got stuck=%v guidance=%q, want not stuck", stuck, guidance)
		}
	})
}

func TestStuckDetectorErrorStreakTriggers(t *testing.T) {
	d := &StuckDetector{}
	var events []Event
	for i := 0; i < stuckErrorStreak; i++ {
		events = append(events, Event{Kind: EventError, Err: fmt.Sprintf("failure %d", i)})
	}
	guidance, stuck := d.Observe(events)
	if !stuck {
		t.Fatal("five consecutive error events should be detected as stuck")
	}
	if guidance != stuckGuidanceError {
		t.Fatalf("guidance = %q, want %q", guidance, stuckGuidanceError)
	}

	// 中间一个成功的步骤会打断连续序列。
	d.Reset()
	events = append(events[:stuckErrorStreak-1], Event{Kind: EventTextDelta, Text: "recovered"})
	guidance, stuck = d.Observe(events)
	if stuck || guidance != "" {
		t.Fatalf("broken error streak: got stuck=%v guidance=%q, want not stuck", stuck, guidance)
	}
}

func TestStuckDetectorAlternatingCallsTrigger(t *testing.T) {
	d := &StuckDetector{}
	a := stuckToolPair("a", "read_file", `{"path":"a.go"}`, false)
	b := stuckToolPair("b", "read_file", `{"path":"b.go"}`, false)
	var events []Event
	for i := 0; i < stuckAlternationLen/2; i++ {
		events = append(events, a...)
		events = append(events, b...)
	}
	guidance, stuck := d.Observe(events)
	if !stuck {
		t.Fatal("A/B/A/B tool calls should be detected as stuck")
	}
	if guidance != stuckGuidanceAlt {
		t.Fatalf("guidance = %q, want %q", guidance, stuckGuidanceAlt)
	}
}

func TestStuckDetectorMonologueTriggers(t *testing.T) {
	d := &StuckDetector{}
	var events []Event
	for i := 0; i < stuckMonologueLen; i++ {
		events = append(events, Event{Kind: EventTextDelta, Text: fmt.Sprintf("paragraph %d", i)})
	}
	guidance, stuck := d.Observe(events)
	if !stuck {
		t.Fatal("five text deltas with no tool call should be detected as stuck")
	}
	if guidance != stuckGuidanceText {
		t.Fatalf("guidance = %q, want %q", guidance, stuckGuidanceText)
	}

	// 中间一次工具调用会打断独白。
	d.Reset()
	events = append(events[:stuckMonologueLen-1],
		stuckToolPair("call", "read_file", `{}`, false)...)
	guidance, stuck = d.Observe(events)
	if stuck || guidance != "" {
		t.Fatalf("broken monologue: got stuck=%v guidance=%q, want not stuck", stuck, guidance)
	}
}

func TestStuckDetectorVariedEventsNotStuck(t *testing.T) {
	d := &StuckDetector{}
	events := []Event{
		{Kind: EventTurnStart, Turn: 1},
	}
	events = append(events, stuckToolPair("1", "read_file", `{"path":"a"}`, false)...)
	events = append(events, stuckToolPair("2", "read_file", `{"path":"a"}`, false)...) // 两次重复，低于阈值
	events = append(events, stuckToolPair("3", "read_file", `{"path":"b"}`, false)...)
	events = append(events, Event{Kind: EventTextDelta, Text: "here is what I found"})
	events = append(events, Event{Kind: EventError, Err: "one transient failure"})
	events = append(events, stuckToolPair("4", "write_file", `{"path":"c"}`, false)...)
	events = append(events, Event{Kind: EventTurnEnd, Turn: 1})

	guidance, stuck := d.Observe(events)
	if stuck || guidance != "" {
		t.Fatalf("varied events: got stuck=%v guidance=%q, want not stuck", stuck, guidance)
	}
}

func TestStuckDetectorOneShot(t *testing.T) {
	events := stuckRepeatedCalls(stuckRepeatCalls)
	d := &StuckDetector{}

	guidance, stuck := d.Observe(events)
	if !stuck || guidance == "" {
		t.Fatalf("first observation: got stuck=%v guidance=%q, want a nudge", stuck, guidance)
	}
	guidance, stuck = d.Observe(events)
	if !stuck || guidance != "" {
		t.Fatalf("second observation: got stuck=%v guidance=%q, want stuck with no repeat nudge", stuck, guidance)
	}

	// 一次健康的观察会为下一个情节重新武装一次性提示。
	if guidance, stuck = d.Observe([]Event{{Kind: EventTextDelta, Text: "done"}}); stuck || guidance != "" {
		t.Fatalf("healthy observation: got stuck=%v guidance=%q, want not stuck", stuck, guidance)
	}
	guidance, stuck = d.Observe(events)
	if !stuck || guidance == "" {
		t.Fatalf("after healthy observation: got stuck=%v guidance=%q, want a fresh nudge", stuck, guidance)
	}
}

func TestStuckDetectorResetReArms(t *testing.T) {
	events := stuckRepeatedCalls(stuckRepeatCalls)
	d := &StuckDetector{}

	if guidance, _ := d.Observe(events); guidance == "" {
		t.Fatal("first observation should nudge")
	}
	if guidance, _ := d.Observe(events); guidance != "" {
		t.Fatal("second observation must not repeat the nudge")
	}
	d.Reset()
	guidance, stuck := d.Observe(events)
	if !stuck || guidance == "" {
		t.Fatalf("after Reset: got stuck=%v guidance=%q, want a fresh nudge", stuck, guidance)
	}
}

func TestStuckDetectorWindowIgnoresEventsBeforeUserMessage(t *testing.T) {
	notice := Event{Kind: EventNotice, Text: "steering: try a completely different approach"}

	t.Run("stuck only before the user message", func(t *testing.T) {
		events := stuckRepeatedCalls(stuckRepeatCalls)
		events = append(events, notice)
		events = append(events, Event{Kind: EventTextDelta, Text: "understood"})
		events = append(events, stuckToolPair("new", "list_dir", `{}`, false)...)

		d := &StuckDetector{}
		guidance, stuck := d.Observe(events)
		if stuck || guidance != "" {
			t.Fatalf("got stuck=%v guidance=%q, want not stuck", stuck, guidance)
		}
	})

	t.Run("stuck after the user message", func(t *testing.T) {
		events := []Event{{Kind: EventNotice, Text: "steering: continue"}}
		events = append(events, stuckRepeatedCalls(stuckRepeatCalls)...)

		d := &StuckDetector{}
		guidance, stuck := d.Observe(events)
		if !stuck || guidance != stuckGuidanceRepeat {
			t.Fatalf("got stuck=%v guidance=%q, want repeat nudge", stuck, guidance)
		}
	})
}

func TestStuckDetectorWindowExcludesOldEvents(t *testing.T) {
	// 重复位于一个长于窗口的切片的开头；只有尾部的不同事件可见，因此不应报告任何内容。
	// （接上行）
	events := stuckRepeatedCalls(stuckRepeatCalls)
	for i := 0; i < stuckWindow; i++ {
		events = append(events, Event{Kind: EventToolStart, ToolCallID: fmt.Sprintf("other-%d", i), ToolName: "x", Args: fmt.Sprintf("%d", i)})
	}
	d := &StuckDetector{}
	guidance, stuck := d.Observe(events)
	if stuck || guidance != "" {
		t.Fatalf("got stuck=%v guidance=%q, want not stuck (repeat outside window)", stuck, guidance)
	}
}
