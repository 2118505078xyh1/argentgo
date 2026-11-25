package app

import (
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/hooks"
)

func TestHookLifecycleEnabled(t *testing.T) {
	if !hookLifecycleEnabled(nil) {
		t.Fatal("nil env must default to on")
	}
	if !hookLifecycleEnabled(func(string) string { return "" }) {
		t.Fatal("default on")
	}
	if !hookLifecycleEnabled(func(string) string { return " 1 " }) {
		t.Fatal("env=1 on")
	}
	if hookLifecycleEnabled(func(string) string { return " 0 " }) {
		t.Fatal("env=0 off")
	}
}

func TestLifecycleHookEventMapping(t *testing.T) {
	cases := []struct {
		event agent.Event
		want  hooks.Event
		ok    bool
	}{
		{agent.Event{Kind: agent.EventTurnStart}, hooks.TurnStart, true},
		{agent.Event{Kind: agent.EventTurnEnd}, hooks.EventTurnEnd, true},
		{agent.Event{Kind: agent.EventSubagentEnd}, hooks.SubagentStop, true},
		{agent.Event{Kind: agent.EventStop, Reason: agent.StopCancelled}, hooks.EventInterrupt, true},
		{agent.Event{Kind: agent.EventStop, Reason: agent.StopProviderError}, hooks.EventStopFailure, true},
		{agent.Event{Kind: agent.EventStop, Reason: agent.StopComplete}, hooks.EventTurnEnd, true},
		{agent.Event{Kind: agent.EventTextDelta}, "", false},
	}
	for _, tc := range cases {
		got, ok := lifecycleHookEvent(tc.event)
		if ok != tc.ok || got != tc.want {
			t.Errorf("event %+v -> (%q,%v), want (%q,%v)", tc.event, got, ok, tc.want, tc.ok)
		}
	}
}
