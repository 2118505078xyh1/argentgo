package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestKillRing(t *testing.T) {
	t.Run("consecutive backward kills accumulate in order", func(t *testing.T) {
		r := newKillRing()
		r.Kill("c", killBackward, true)
		r.Kill("b", killBackward, true)
		r.Kill("a", killBackward, true)
		if got, want := r.Peek(), "abc"; got != want {
			t.Fatalf("peek = %q, want %q", got, want)
		}
		if r.Len() != 1 {
			t.Fatalf("len = %d, want 1", r.Len())
		}
	})

	t.Run("consecutive forward kills append", func(t *testing.T) {
		r := newKillRing()
		r.Kill("a", killForward, true)
		r.Kill("b", killForward, true)
		if got, want := r.Peek(), "ab"; got != want {
			t.Fatalf("peek = %q, want %q", got, want)
		}
	})

	t.Run("direction change starts a new entry and yank-pop cycles", func(t *testing.T) {
		r := newKillRing()
		r.Kill("A", killBackward, true)
		r.Kill("B", killForward, false) // 新片段
		if r.Len() != 2 {
			t.Fatalf("len = %d, want 2", r.Len())
		}
		if got := r.Yank(); got != "B" {
			t.Fatalf("yank = %q, want B", got)
		}
		if got := r.YankPop(); got != "A" {
			t.Fatalf("yank-pop = %q, want A", got)
		}
		if got := r.YankPop(); got != "B" {
			t.Fatalf("yank-pop should wrap to most recent, got %q", got)
		}
	})

	t.Run("break chain splits consecutive kills", func(t *testing.T) {
		r := newKillRing()
		r.Kill("x", killBackward, true)
		r.BreakChain()
		r.Kill("y", killBackward, true)
		if r.Len() != 2 || r.Peek() != "y" {
			t.Fatalf("entries = %d, peek = %q", r.Len(), r.Peek())
		}
	})

	t.Run("bounded by rune count without splitting CJK", func(t *testing.T) {
		r := newKillRing()
		r.Kill(strings.Repeat("汉", killRingMaxRunes+100), killBackward, false)
		peek := r.Peek()
		if utf8.RuneCountInString(peek) != killRingMaxRunes {
			t.Fatalf("rune count = %d, want %d", utf8.RuneCountInString(peek), killRingMaxRunes)
		}
		if !utf8.ValidString(peek) {
			t.Fatal("clipped kill is not valid UTF-8")
		}
	})

	t.Run("capacity drops oldest entries", func(t *testing.T) {
		r := newKillRing()
		for i := 0; i < killRingCapacity+10; i++ {
			r.Kill("x", killBackward, false)
		}
		if r.Len() != killRingCapacity {
			t.Fatalf("len = %d, want %d", r.Len(), killRingCapacity)
		}
	})
}

func TestKillRingKey(t *testing.T) {
	t.Run("backspace twice then ctrl+y restores merged kill", func(t *testing.T) {
		m := New(Config{})
		m.editor.SetText("abc")
		m.HandleKey(Key{Kind: KindBackspace}) // 删 c
		m.HandleKey(Key{Kind: KindBackspace}) // 删 b
		if got := m.editor.Text(); got != "a" {
			t.Fatalf("text after kills = %q, want a", got)
		}
		if got := m.kill.Peek(); got != "bc" {
			t.Fatalf("kill = %q, want bc", got)
		}
		m.HandleKey(Key{Kind: KindCtrlY})
		if got := m.editor.Text(); got != "abc" {
			t.Fatalf("text after yank = %q, want abc", got)
		}
	})

	t.Run("alt+y cycles through separate kills", func(t *testing.T) {
		m := New(Config{})
		m.editor.SetText("aXbY")
		m.HandleKey(Key{Kind: KindBackspace}) // 删 Y
		m.HandleKey(Key{Kind: KindLeft})      // 断链
		m.HandleKey(Key{Kind: KindBackspace}) // 删 X
		if m.kill.Len() != 2 {
			t.Fatalf("kill len = %d, want 2", m.kill.Len())
		}
		m.HandleKey(Key{Kind: KindCtrlY}) // 取回 X → "aXb"
		m.HandleKey(Key{Kind: KindAltY})  // 循环到更早的 Y → "aXYb"
		if got := m.editor.Text(); got != "aXYb" {
			t.Fatalf("text after yank/pop = %q, want aXYb", got)
		}
	})

	t.Run("decoder maps ctrl+y and alt+y", func(t *testing.T) {
		d := &Decoder{}
		keys := d.Feed([]byte{0x19})
		if len(keys) != 1 || keys[0].Kind != KindCtrlY {
			t.Fatalf("ctrl+y = %#v", keys)
		}
		d = &Decoder{}
		keys = d.Feed([]byte("\x1by"))
		if len(keys) != 1 || keys[0].Kind != KindAltY {
			t.Fatalf("alt+y = %#v", keys)
		}
	})

	t.Run("ARGENT_KILL_RING=0 disables recording", func(t *testing.T) {
		m := New(Config{Env: func(key string) string {
			if key == "ARGENT_KILL_RING" {
				return "0"
			}
			return ""
		}})
		m.editor.SetText("abc")
		m.HandleKey(Key{Kind: KindBackspace})
		if got := m.editor.Text(); got != "ab" {
			t.Fatalf("delete should still work with kill ring off, got %q", got)
		}
		if m.kill.Len() != 0 {
			t.Fatalf("kill ring should stay empty when disabled, len = %d", m.kill.Len())
		}
	})
}
