//go:build windows

package nativegui

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/gui"
)

func TestApplyEventCoalescesStreamedAssistantText(t *testing.T) {
	s := &state{}
	applyEvent(s, gui.Event{Kind: gui.EventAgent, Agent: &gui.AgentEvent{Type: gui.AgentText, Text: "hel"}})
	applyEvent(s, gui.Event{Kind: gui.EventAgent, Agent: &gui.AgentEvent{Type: gui.AgentText, Text: "lo"}})
	if len(s.rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(s.rows))
	}
	if s.rows[0].kind != "assistant" || s.rows[0].text != "hello" {
		t.Fatalf("row = %+v, want one assistant row with 'hello'", s.rows[0])
	}
}

func TestApplyEventKeepsAttemptsOutOfTheTranscriptText(t *testing.T) {
	s := &state{}
	applyEvent(s, gui.Event{Kind: gui.EventAgent, Agent: &gui.AgentEvent{
		Type: gui.AgentAttempt, Attempt: "retried", Text: "retrying tool",
	}})
	if len(s.rows) != 1 || s.rows[0].role != "retried" {
		t.Fatalf("rows = %+v, want one attempt notice", s.rows)
	}
}

func TestApplyEventTurnDoneClearsBusy(t *testing.T) {
	s := &state{busy: true}
	applyEvent(s, gui.Event{Kind: gui.EventTurnDone, TurnDone: &gui.TurnDone{Requests: 2, TotalTokens: 40}})
	if s.busy || s.status == "" {
		t.Fatalf("busy=%v status=%q, want idle with a status", s.busy, s.status)
	}
}

func TestFirstLineTruncatesToolOutput(t *testing.T) {
	if got := firstLine("ok\nmore"); got != "ok" {
		t.Fatalf("firstLine = %q, want 'ok'", got)
	}
}
