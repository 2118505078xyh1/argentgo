package tui

import (
	"context"
	"strings"
	"testing"
)

func typeCommand(m *Model, command string) KeyAction {
	m.HandleKey(Key{Kind: KindPaste, Text: command})
	return m.HandleKey(Key{Kind: KindEnter})
}

func frameText(m *Model, width, height int) string {
	buf := m.Frame(width, height)
	var out strings.Builder
	for y := 0; y < buf.Height(); y++ {
		out.WriteString(buf.RowText(y))
		out.WriteByte('\n')
	}
	return out.String()
}

func TestUndoRetryAndNameCommandsReturnActions(t *testing.T) {
	called := map[string]string{}
	action := func(kind string) TurnAction {
		return func(_ context.Context, arg string) error {
			called[kind] = arg
			return nil
		}
	}
	cases := []struct {
		command string
		want    KeyAction
		arg     string
		kind    string
	}{
		{"/undo", ActionUndo, "", "undo"},
		{"/retry 2", ActionRetry, "2", "retry"},
		{"/name 新 标题", ActionRename, "新 标题", "rename"},
	}
	for _, tc := range cases {
		m := New(Config{})
		m.SetTurnActions(action("undo"), action("retry"), action("rename"))
		if got := typeCommand(m, tc.command); got != tc.want {
			t.Fatalf("%q action = %v, want %v", tc.command, got, tc.want)
		}
		if got := m.TakeActionArg(); got != tc.arg {
			t.Fatalf("%q arg = %q, want %q", tc.command, got, tc.arg)
		}
		if got := called[tc.kind]; got != "" {
			t.Fatalf("the run loop, not the model, should invoke the hook; got %q", got)
		}
	}
}

func TestCommandsWithoutAHookExplainTheMissingWiring(t *testing.T) {
	m := New(Config{})
	if action := typeCommand(m, "/undo"); action != ActionNone {
		t.Fatalf("unwired /undo returned %v", action)
	}
	if !strings.Contains(m.Plain(), "checkpoint hook") {
		t.Fatalf("missing hook notice: %s", m.Plain())
	}
}

func TestBusyTurnRefusesUndo(t *testing.T) {
	m := New(Config{})
	m.SetTurnActions(func(context.Context, string) error { return nil }, nil, nil)
	m.working = true
	if action := typeCommand(m, "/undo"); action != ActionNone {
		t.Fatalf("busy /undo returned %v", action)
	}
}

func TestAttemptBadgesAppearInTranscriptAndPlainText(t *testing.T) {
	m := New(Config{})
	m.AppendAttempt(AttemptFailed, "tool boom")
	m.AppendTool("bash", "{}", "", true)
	entries := m.Transcript()
	if entries[0].Attempt != AttemptFailed || entries[1].Attempt != AttemptFailed {
		t.Fatalf("attempt metadata missing: %+v", entries)
	}
	text := frameText(m, 100, 24)
	if !strings.Contains(text, "failed") || !strings.Contains(text, "tool boom") {
		t.Fatalf("attempt badge not painted: %s", text)
	}
	if !strings.Contains(m.Plain(), "[failed]") {
		t.Fatalf("plain transcript lost the badge: %s", m.Plain())
	}
}

func TestAttemptEventAddsBadge(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{Kind: EventAttempt, Attempt: AttemptStreamError, Detail: "sse reset"})
	if got := m.Transcript()[0].Attempt; got != AttemptStreamError {
		t.Fatalf("event attempt = %q", got)
	}
}

func TestSessionRailShowsGeneratedTitles(t *testing.T) {
	m := New(Config{Sessions: []string{"sessions/会议记录.jsonl"}})
	m.HandleKey(Key{Kind: KindCtrlS})
	text := frameText(m, 100, 24)
	if !strings.Contains(text, "会议记录") || !strings.Contains(text, "SESSIONS") {
		t.Fatalf("session rail missing title: %s", text)
	}
	m.SetSessions([]SessionInfo{{Path: "a.jsonl", Title: "renamed"}})
	text = frameText(m, 100, 24)
	if !strings.Contains(text, "renamed") || strings.Contains(text, "会议记录") {
		t.Fatalf("session rail did not update: %s", text)
	}
}

func TestCachedTokensAppearInHeaderAndStatusLine(t *testing.T) {
	m := New(Config{})
	m.SetUsageVisible(true)
	m.SetUsage(1200, 300)
	m.SetCachedTokens(900)
	text := frameText(m, 110, 24)
	if matches := strings.Count(text, "cached 900"); matches < 2 {
		t.Fatalf("cached count should appear in header and status, got %d: %s", matches, text)
	}
}

func TestHelpMentionsUndoRetryAndCommandArguments(t *testing.T) {
	for _, locale := range []string{"en", "zh"} {
		help := text(locale, "help")
		for _, want := range []string{"/undo", "/retry", "/name", "$ARGUMENTS"} {
			if !strings.Contains(help, want) {
				t.Errorf("locale %s help missing %q: %s", locale, want, help)
			}
		}
	}
}

// TestHelpMentionsExternalEditor 确保两种 locale 的帮助串都提示
// Ctrl+G 外部编辑器快捷键，同时守住原有 /undo、/retry、$ARGUMENTS 文案。
func TestHelpMentionsExternalEditor(t *testing.T) {
	for _, locale := range []string{"en", "zh"} {
		help := text(locale, "help")
		for _, want := range []string{"ctrl+g", "/undo", "/retry", "/name", "$ARGUMENTS"} {
			if !strings.Contains(help, want) {
				t.Errorf("locale %s help missing %q: %s", locale, want, help)
			}
		}
	}
}

func TestAltUndoAndRetryKeysDecode(t *testing.T) {
	decoder := &Decoder{}
	keys := decoder.Feed([]byte{0x1b, 'u', 0x1b, 'r'})
	if len(keys) != 2 || keys[0].Kind != KindAltU || keys[1].Kind != KindAltR {
		t.Fatalf("alt keys = %+v", keys)
	}
	if decoder.Pending() {
		t.Fatalf("decoder kept bytes after complete alt keys")
	}
}

func TestFailedToolEndMarksTheTranscriptEntry(t *testing.T) {
	m := New(Config{})
	m.AppendTool("write", "{}", "ok", false)
	m.HandleEvent(Event{Kind: EventToolEnd, ToolCallID: "1", ToolName: "write", IsError: true})
	entries := m.Transcript()
	if entries[0].Attempt != AttemptFailed {
		t.Fatalf("tool end did not badge the entry: %+v", entries[0])
	}
}
