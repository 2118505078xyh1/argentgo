package app

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestEntriesToTUI 验证会话历史到 TUI 条目的映射：user/assistant/reasoning/tool
// 各有对应条目，system/未知角色被跳过，工具卡片为静态（非运行中）。
func TestEntriesToTUI(t *testing.T) {
	in := []session.Entry{
		{Role: "system", Content: "你是一个助手"},
		{Role: "user", Content: "读一下文件"},
		{Role: "assistant", Content: "好的", Reasoning: "先看看内容"},
		{Role: "tool", Name: "read", Content: "file content", ToolCallID: "c1"},
		{Role: "assistant", Content: "内容已读"},
	}
	got := entriesToTUI(in)
	if len(got) != 5 {
		t.Fatalf("entries = %+v, want 5 (user + reasoning + assistant + tool + assistant)", got)
	}
	if got[0].Kind != tui.EntryUser || got[0].Text != "读一下文件" {
		t.Fatalf("user mapping wrong: %+v", got[0])
	}
	if got[1].Kind != tui.EntryReasoning || got[1].Text != "先看看内容" {
		t.Fatalf("reasoning mapping wrong: %+v", got[1])
	}
	if got[2].Kind != tui.EntryAssistant || got[2].Text != "好的" {
		t.Fatalf("assistant mapping wrong: %+v", got[2])
	}
	if got[3].Kind != tui.EntryTool || got[3].Name != "read" || got[3].Running {
		t.Fatalf("tool mapping wrong (must be static): %+v", got[3])
	}
	if !strings.Contains(got[3].Output, "file content") {
		t.Fatalf("tool output missing: %+v", got[3])
	}
}

// TestEntriesToTUIBounded 验证超长历史正文被裁剪（避免首帧卡顿/内存暴涨）。
func TestEntriesToTUIBounded(t *testing.T) {
	long := strings.Repeat("字", maxHistoryEntryRunes+500)
	got := entriesToTUI([]session.Entry{{Role: "assistant", Content: long}})
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	if len([]rune(got[0].Text)) > maxHistoryEntryRunes+2 {
		t.Fatalf("history not bounded: %d runes", len([]rune(got[0].Text)))
	}
}
