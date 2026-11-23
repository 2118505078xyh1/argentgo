package tui

import (
	"strings"
	"testing"
)

// TestGoalStatusEventReachesTranscript 证明 goal_status 事件不再被 HandleEvent
// 静默丢弃：状态在 zh/en 两种 locale 下都变成一条有界通知。
func TestGoalStatusEventReachesTranscript(t *testing.T) {
	for _, tc := range []struct {
		locale string
		want   string
		forbid string
	}{
		{locale: "zh", want: "目标循环：运行中", forbid: "goal loop"},
		{locale: "en", want: "goal loop: running", forbid: "目标循环"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			m := New(Config{Locale: tc.locale})
			m.HandleEvent(Event{
				Kind:              TuiEventGoalStatus,
				GoalState:         "running",
				GoalIteration:     3,
				GoalMaxIterations: 10,
			})
			entries := m.Transcript()
			if len(entries) != 1 {
				t.Fatalf("transcript has %d entries, want 1", len(entries))
			}
			got := entries[0]
			if got.Kind != EntryNotice {
				t.Fatalf("entry kind = %v, want EntryNotice", got.Kind)
			}
			if !strings.Contains(got.Text, tc.want) || !strings.Contains(got.Text, "3/10") {
				t.Fatalf("notice %q missing state %q or count", got.Text, tc.want)
			}
			if strings.Contains(got.Text, tc.forbid) {
				t.Fatalf("notice %q leaked wrong-locale text", got.Text)
			}
		})
	}
}

// TestGoalStatusEventBoundsUnknownState 证明未知状态有界透传，既不丢状态，
// 也不会让异常长负载撑宽 transcript。
func TestGoalStatusEventBoundsUnknownState(t *testing.T) {
	m := New(Config{Locale: "en"})
	m.HandleEvent(Event{
		Kind:              TuiEventGoalStatus,
		GoalState:         strings.Repeat("状态", 200),
		GoalIteration:     1,
		GoalMaxIterations: 2,
	})
	entries := m.Transcript()
	if len(entries) != 1 {
		t.Fatalf("transcript has %d entries, want 1", len(entries))
	}
	if len([]rune(entries[0].Text)) > maxNoticeDetail {
		t.Fatalf("notice too long: %d runes", len([]rune(entries[0].Text)))
	}
}
