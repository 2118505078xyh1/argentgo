package tui

import (
	"context"
	"testing"
)

// 会话栏从“只能看”变成可操作：Enter 打开、Ctrl+D 删除、Ctrl+R 改名。
func TestSessionRailActions(t *testing.T) {
	m := New(Config{})
	m.SetSessions([]SessionInfo{{Path: "abc", Title: "会议记录"}})
	m.SetSessionActions(
		func(context.Context, string) error { return nil },
		func(context.Context, string) error { return nil },
		func(context.Context, string) error { return nil },
	)

	m.showSessions = true
	if action := m.HandleKey(Key{Kind: KindEnter}); action != ActionOpenSession || m.TakeActionArg() != "abc" {
		t.Fatalf("Enter action = %v, arg = %q, want open of abc", action, m.TakeActionArg())
	}
	if action := m.HandleKey(Key{Kind: KindCtrlD}); action != ActionDeleteSession || m.TakeActionArg() != "abc" {
		t.Fatalf("Ctrl+D action = %v, arg = %q, want delete of abc", action, m.TakeActionArg())
	}
	m.HandleKey(Key{Kind: KindCtrlR})
	if !m.renamePending {
		t.Fatal("Ctrl+R must start the rename prompt")
	}
	for _, r := range "新标题" {
		m.HandleKey(Key{Kind: KindRune, Rune: r})
	}
	if action := m.HandleKey(Key{Kind: KindEnter}); action != ActionRenameSession || m.TakeActionArg() != "abc\x00新标题" {
		t.Fatalf("rename action = %v, arg = %q, want abc + new title", action, m.TakeActionArg())
	}
	if m.renamePending {
		t.Fatal("rename prompt must be cleared after confirmation")
	}
}
