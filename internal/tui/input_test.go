package tui

import (
	"context"
	"strings"
	"testing"
)

// typeBytes 把原始字节流喂给解码器并进入模型，
// 这与终端走的路径相同。
func typeBytes(m *Model, input string) []KeyAction {
	decoder := &Decoder{}
	keys := decoder.Feed([]byte(input))
	keys = append(keys, decoder.FlushEscape()...)
	actions := make([]KeyAction, 0, len(keys))
	for _, key := range keys {
		actions = append(actions, m.HandleKey(key))
	}
	return actions
}

func TestTypingInsertsRunes(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "hello 你好")
	if got := m.Input(); got != "hello 你好" {
		t.Fatalf("input = %q, want hello 你好", got)
	}
	if got := m.Cursor(); got != 8 {
		t.Fatalf("cursor = %d, want 8 runes", got)
	}
}

func TestEditingKeys(t *testing.T) {
	cases := []struct {
		name   string
		keys   string
		want   string
		cursor int
	}{
		{"backspace", "abc\x7f", "ab", 2},
		{"delete at caret", "abc\x1b[D\x1b[D\x1b[3~", "ac", 1},
		{"left then insert", "ac\x1b[Db", "abc", 2},
		{"home then insert", "bc\x1b[Ha", "abc", 1},
		{"end then insert", "ab\x1b[Fc", "abc", 3},
		{"ctrl+a", "abc\x01", "abc", 0},
		{"ctrl+e", "abc\x01\x05", "abc", 3},
		{"ctrl+u kills to start", "abcdef\x01\x1b[C\x1b[C\x15", "cdef", 0},
		{"ctrl+k kills to end", "abcdef\x01\x1b[C\x1b[C\x0b", "ab", 2},
		{"ctrl+w kills the word", "foo bar\x17", "foo ", 4},
		{"delete on empty line", "\x1b[3~", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Config{})
			typeBytes(m, tc.keys)
			if got := m.Input(); got != tc.want {
				t.Fatalf("input = %q, want %q", got, tc.want)
			}
			if got := m.Cursor(); got != tc.cursor {
				t.Fatalf("cursor = %d, want %d", got, tc.cursor)
			}
		})
	}
}

func TestBackspaceKeepsWideRunesWhole(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "你好\x7f")
	if got := m.Input(); got != "你" {
		t.Fatalf("input = %q, want 你", got)
	}
}

func TestSubmitMovesTextIntoTheTranscript(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "write a test")
	actions := typeBytes(m, "\r")
	if len(actions) == 0 || actions[len(actions)-1] != ActionSubmit {
		t.Fatalf("enter should submit, got %v", actions)
	}
	if got := m.TakeSubmitted(); got != "write a test" {
		t.Fatalf("submitted = %q", got)
	}
	if m.Input() != "" {
		t.Fatalf("input should be cleared after submit, got %q", m.Input())
	}
	entries := m.Transcript()
	if len(entries) != 1 || entries[0].Kind != EntryUser || entries[0].Text != "write a test" {
		t.Fatalf("transcript = %+v, want one user entry", entries)
	}
}

func TestEnterOnBlankInputDoesNothing(t *testing.T) {
	m := New(Config{})
	if action := m.HandleKey(Key{Kind: KindEnter}); action != ActionNone {
		t.Fatalf("action = %v, want ActionNone", action)
	}
	if len(m.Transcript()) != 0 {
		t.Fatalf("blank submit should not add an entry")
	}
}

func TestNewlineKeysInsertLineBreaks(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "one\ntwo")
	if got := m.Input(); got != "one\ntwo" {
		t.Fatalf("ctrl+j input = %q, want a two-line buffer", got)
	}

	alt := New(Config{})
	typeBytes(alt, "one\x1b\rtwo")
	if got := alt.Input(); got != "one\ntwo" {
		t.Fatalf("alt+enter input = %q, want a two-line buffer", got)
	}
}

func TestUpAndDownWalkTheHistory(t *testing.T) {
	m := New(Config{})
	for _, text := range []string{"first", "second"} {
		typeBytes(m, text+"\r")
		m.EndTurn(nil)
		m.HandleKey(Key{Kind: KindRune, Rune: 'x'})
		m.HandleKey(Key{Kind: KindBackspace})
	}
	typeBytes(m, "\x1b[A")
	if got := m.Input(); got != "second" {
		t.Fatalf("first up = %q, want second", got)
	}
	typeBytes(m, "\x1b[A")
	if got := m.Input(); got != "first" {
		t.Fatalf("second up = %q, want first", got)
	}
	typeBytes(m, "\x1b[B")
	if got := m.Input(); got != "second" {
		t.Fatalf("down = %q, want second", got)
	}
	typeBytes(m, "\x1b[B")
	if got := m.Input(); got != "" {
		t.Fatalf("down past the end should restore the empty draft, got %q", got)
	}
}

func TestEditingRecalledHistorySurvivesDown(t *testing.T) {
	m := New(Config{})
	for _, text := range []string{"first", "second"} {
		typeBytes(m, text+"\r")
		m.EndTurn(nil)
		m.HandleKey(Key{Kind: KindRune, Rune: 'x'})
		m.HandleKey(Key{Kind: KindBackspace})
	}
	typeBytes(m, "\x1b[A") // recall "second"
	if got := m.Input(); got != "second" {
		t.Fatalf("up = %q, want second", got)
	}
	typeBytes(m, " edited") // edit the recalled line
	if got := m.Input(); got != "second edited" {
		t.Fatalf("after edit = %q", got)
	}
	typeBytes(m, "\x1b[B") // down must not snap back to the stale draft
	if got := m.Input(); got != "second edited" {
		t.Fatalf("down after editing history wiped the edit: %q", got)
	}
}

func TestUpAndDownMoveInsideAMultilineBuffer(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "ab\ncd")
	if action := m.HandleKey(Key{Kind: KindUp}); action != ActionNone {
		t.Fatalf("up: %v", action)
	}
	if got := m.Cursor(); got != 2 {
		t.Fatalf("cursor = %d, want 2 (end of the first line)", got)
	}
	if got := m.Input(); got != "ab\ncd" {
		t.Fatalf("up inside a multiline buffer must not replace the text, got %q", got)
	}
}

func TestCtrlCCancelsThenQuits(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "hello\r")
	if !m.Busy() {
		t.Fatalf("submitting should mark the model busy")
	}
	if action := m.HandleKey(Key{Kind: KindCtrlC}); action != ActionAbort {
		t.Fatalf("ctrl+c during a turn = %v, want ActionAbort", action)
	}
	m.EndTurn(context.Canceled)
	if action := m.HandleKey(Key{Kind: KindCtrlC}); action != ActionQuit {
		t.Fatalf("second ctrl+c = %v, want ActionQuit", action)
	}
	if !m.Quit() {
		t.Fatalf("quit flag should be set")
	}
}

func TestCtrlCClearsTypedTextFirst(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "draft")
	if action := m.HandleKey(Key{Kind: KindCtrlC}); action != ActionNone {
		t.Fatalf("ctrl+c with text = %v, want ActionNone", action)
	}
	if m.Input() != "" {
		t.Fatalf("ctrl+c should clear the input first")
	}
}

func TestScrollKeysMoveTheTranscript(t *testing.T) {
	m := New(Config{})
	for i := 0; i < 50; i++ {
		m.AppendNotice("line")
	}
	buffer := m.Frame(80, 24)
	if buffer.Height() != 24 {
		t.Fatalf("frame height = %d", buffer.Height())
	}
	m.HandleKey(Key{Kind: KindPageUp})
	if m.scroll <= 0 {
		t.Fatalf("page up should scroll back, scroll = %d", m.scroll)
	}
	m.HandleKey(Key{Kind: KindPageDown})
	m.HandleKey(Key{Kind: KindPageDown})
	if m.scroll < 0 {
		t.Fatalf("scroll must not go negative, got %d", m.scroll)
	}
}

func TestSlashCommands(t *testing.T) {
	m := New(Config{})
	m.AppendNotice("keep me")
	typeBytes(m, "/clear\r")
	if got := m.Transcript(); len(got) != 0 {
		t.Fatalf("clear left %d entries", len(got))
	}
	if actions := typeBytes(m, "/quit\r"); len(actions) == 0 || actions[len(actions)-1] != ActionQuit {
		t.Fatalf("/quit should quit, got %v", actions)
	}
}

func TestPasteIsInsertedAsOneBlock(t *testing.T) {
	m := New(Config{})
	typeBytes(m, "\x1b[200~one\ntwo\x1b[201~")
	if got := m.Input(); !strings.Contains(got, "one") || !strings.Contains(got, "two") {
		t.Fatalf("input = %q, want the pasted block", got)
	}
}
