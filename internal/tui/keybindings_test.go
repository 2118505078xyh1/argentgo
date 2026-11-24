package tui

import (
	"testing"
)

func TestKeyCanonical(t *testing.T) {
	cases := []struct {
		key  Key
		want string
	}{
		{Key{Kind: KindRune, Rune: 'a'}, "a"},
		{Key{Kind: KindRune, Rune: '中'}, "中"},
		{Key{Kind: KindEnter}, "enter"},
		{Key{Kind: KindNewline}, "ctrl+j"},
		{Key{Kind: KindCtrlT}, "ctrl+t"},
		{Key{Kind: KindAltB}, "alt+b"},
		{Key{Kind: KindCtrlHome}, "ctrl+home"},
		{Key{Kind: KindPageUp}, "pgup"},
		{Key{Kind: KindEsc}, "esc"},
		{Key{Kind: KindMouseWheelUp}, "mouse_wheel_up"},
		{Key{Kind: KindIgnore}, ""},
	}
	for _, tc := range cases {
		if got := tc.key.Canonical(); got != tc.want {
			t.Errorf("Canonical(%v) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestParseKeySpecRoundTrip(t *testing.T) {
	// 所有有专属 Kind 的键，Canonical → ParseKeySpec 应往返。
	for kind := KindEnter; kind <= KindAltL; kind++ {
		canonical := (Key{Kind: kind}).Canonical()
		if canonical == "" {
			continue
		}
		got, ok := ParseKeySpec(canonical)
		if !ok || got != kind {
			t.Errorf("ParseKeySpec(%q) = %v,%v, want %v", canonical, got, ok, kind)
		}
	}
	if _, ok := ParseKeySpec("not-a-key"); ok {
		t.Fatal("unknown spec must not parse")
	}
}

func TestDefaultBindingsSemantics(t *testing.T) {
	b := DefaultBindings()
	// 关键键位与实际分派一致（这些是帮助文案曾漂移的点）。
	assertions := []struct {
		key  string
		want ActionID
	}{
		{"ctrl+t", ActToggleThinking}, // 不是复制
		{"ctrl+x", ActCopyLast},       // 复制
		{"alt+b", ActToggleWorkspace}, // 工作区面板
		{"ctrl+b", ActLeft},           // 左移光标
		{"ctrl+c", ActAbort},
		{"enter", ActSubmit},
	}
	for _, a := range assertions {
		got, ok := b.Resolve(a.key)
		if !ok || got != a.want {
			t.Errorf("Resolve(%q) = %q,%v, want %q", a.key, got, ok, a.want)
		}
	}
	// 大小写折叠
	if got, _ := b.Resolve("CTRL+T"); got != ActToggleThinking {
		t.Errorf("case-fold Resolve = %q", got)
	}
	if _, ok := b.Resolve("unbound"); ok {
		t.Fatal("unbound key must not resolve")
	}
}

func TestConflictDetection(t *testing.T) {
	base := DefaultBindings()
	override := Bindings{"ctrl+t": ActCopyLast, "ctrl+z": ActUndo}
	got := Conflict(base, override)
	if len(got) != 1 || got[0] != "ctrl+t" {
		t.Fatalf("conflicts = %v, want [ctrl+t]", got)
	}
	if c := Conflict(base, Bindings{"ctrl+z": ActUndo}); len(c) != 0 {
		t.Fatalf("new key must not conflict: %v", c)
	}
}
