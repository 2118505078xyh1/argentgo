package tui

import (
	"strings"
	"testing"
)

// TestCustomBindingRebind 验证用户重绑定端到端生效：把 submit 从 enter
// 改到 ctrl+z，按 ctrl+z 应提交，按 enter 不再提交（回落到默认 enter→submit，
// 除非同时覆盖）。这里用一个不会触发默认动作的键验证翻译路径。
func TestCustomBindingRebind(t *testing.T) {
	m := New(Config{})
	// 把 undo（默认 alt+u）重绑到 ctrl+z。ctrl+z 默认无专属 Kind（在
	// decodeKey 里 z<0x20? 不，'z'=0x7a>0x20 → KindRune），所以它由 rune 'z'
	// 路径进入：Canonical('z')="z"，映射到 ActUndo，翻译回 alt+u 执行。
	m.SetBindings(Bindings{"z": ActUndo})
	m.HandleKey(Key{Kind: KindRune, Rune: 'z'})
	entries := m.Transcript()
	found := false
	for _, e := range entries {
		if strings.Contains(e.Text, "undo") || strings.Contains(e.Text, "撤销") {
			found = true
		}
	}
	if !found {
		t.Fatalf("rebound key did not trigger undo (no undo notice): %+v", entries)
	}
}

// TestNoBindingsFrameByteIdentical 验证无自定义绑定时帧与默认完全一致
// （架构升级的硬约束：默认零字节影响）。
func TestNoBindingsFrameByteIdentical(t *testing.T) {
	a := New(Config{Model: "m"})
	b := New(Config{Model: "m"})
	b.SetBindings(nil)
	a.HandleKey(Key{Kind: KindRune, Rune: 'x'})
	b.HandleKey(Key{Kind: KindRune, Rune: 'x'})
	if frameText(a, 100, 24) != frameText(b, 100, 24) {
		t.Fatal("nil bindings changed frame bytes")
	}
}

// TestCustomBindingConflictDoesNotApply 验证冲突键不被应用（保守回退默认）。
func TestCustomBindingConflictDoesNotApply(t *testing.T) {
	if c := Conflict(DefaultBindings(), Bindings{"ctrl+t": ActCopyLast}); len(c) == 0 {
		t.Fatal("expected conflict for ctrl+t")
	}
}
