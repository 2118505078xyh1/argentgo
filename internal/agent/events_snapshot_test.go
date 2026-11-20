package agent

import "testing"

// TestRunStateEventsSnapshotIsIndependentCopy 检查 eventsSnapshot 交出的是副本：之后修改底层事件切片，不得改变调用方已收到的内容。
// （接上行）
// （接上行）
func TestRunStateEventsSnapshotIsIndependentCopy(t *testing.T) {
	s := &runState{}

	s.eventsMu.Lock()
	s.events = append(s.events,
		Event{Kind: EventTurnStart, Turn: 1, Text: "first"},
		Event{Kind: EventNotice, Text: "second"},
	)
	s.eventsMu.Unlock()

	snap := s.eventsSnapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot length = %d, want 2", len(snap))
	}
	if snap[0].Text != "first" || snap[1].Text != "second" {
		t.Fatalf("snapshot = %+v, want first and second events", snap)
	}

	// 像之后的一次发出那样修改原始切片：在 eventsMu 下编辑已有条目并追加一条新条目。
	// （接上行）
	s.eventsMu.Lock()
	s.events[0].Text = "changed"
	s.events = append(s.events, Event{Kind: EventStop, Reason: StopComplete})
	s.eventsMu.Unlock()

	if len(snap) != 2 {
		t.Fatalf("snapshot length changed to %d after appending to the source", len(snap))
	}
	if snap[0].Text != "first" {
		t.Fatalf("snapshot[0].Text = %q, want %q (source was mutated)", snap[0].Text, "first")
	}
}
