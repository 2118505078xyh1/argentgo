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

func TestMessageColumnCentersAndCaps(t *testing.T) {
	cases := []struct {
		name         string
		clientW      int
		wantX, wantW int
	}{
		{"wide window is capped at 720", 1180, 350, 720},
		{"medium window uses gutter", 800, 272, 496},
		{"tiny window keeps a minimum", 300, 190, 160},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x, w := messageColumn(tc.clientW)
			if x != tc.wantX || w != tc.wantW {
				t.Fatalf("messageColumn(%d) = (%d, %d), want (%d, %d)", tc.clientW, x, w, tc.wantX, tc.wantW)
			}
		})
	}
}

func TestSpinnerFrameCyclesBraille(t *testing.T) {
	cases := []struct {
		tick int
		want string
	}{
		{0, "⠋"}, {1, "⠙"}, {9, "⠏"}, {10, "⠋"}, {-1, "⠏"},
	}
	for _, tc := range cases {
		if got := spinnerFrame(tc.tick); got != tc.want {
			t.Errorf("spinnerFrame(%d) = %q, want %q", tc.tick, got, tc.want)
		}
	}
}

func TestCursorVisibleAlternatesSlowPhases(t *testing.T) {
	cases := []struct {
		tick int
		want bool
	}{
		{0, true}, {3, true}, {4, false}, {7, false}, {8, true},
	}
	for _, tc := range cases {
		if got := cursorVisible(tc.tick); got != tc.want {
			t.Errorf("cursorVisible(%d) = %v, want %v", tc.tick, got, tc.want)
		}
	}
}

func TestApplyThemeSemanticPalette(t *testing.T) {
	applyTheme(true)
	if clrFaint != rgb(109, 107, 100) || clrBorder != rgb(43, 43, 41) {
		t.Fatalf("dark palette: faint=%06x border=%06x", clrFaint, clrBorder)
	}
	if clrError == clrAccent || clrErrorBg == clrCard || clrStroke == clrBorder {
		t.Fatal("dark palette: semantic colors must stay distinct")
	}
	darkFaint := clrFaint
	applyTheme(false)
	if clrFaint == darkFaint || clrError == clrAccent {
		t.Fatal("light palette: faint/error must differ from dark/accent")
	}
	applyTheme(true)
}

func TestApplyEventTracksToolStateKeepsOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isError bool
		want    string
	}{
		{"success", false, "ok"},
		{"failure", true, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &state{}
			applyEvent(s, gui.Event{Kind: gui.EventAgent, Agent: &gui.AgentEvent{
				Type: gui.AgentToolRequested, Name: "read", Arguments: `{"path":"x"}`,
			}})
			if len(s.rows) != 1 || s.rows[0].state != "running" {
				t.Fatalf("requested rows = %+v, want one running tool", s.rows)
			}
			applyEvent(s, gui.Event{Kind: gui.EventAgent, Agent: &gui.AgentEvent{
				Type: gui.AgentToolFinished, Name: "read", Output: "ok\nmore", IsError: tc.isError,
			}})
			if len(s.rows) != 2 || s.rows[0].state != tc.want || s.rows[1].text != "ok" {
				t.Fatalf("finished rows = %+v, want state %q and output notice", s.rows, tc.want)
			}
		})
	}
}
