package tui

import (
	"strings"
	"testing"
)

// TestSetTranscriptReplacesEntries 验证历史装载：整体替换 transcript、滚动归零，
// 且复制入参（调用方后续改动不影响模型）。这是 resume 显示旧对话的基础 API。
func TestSetTranscriptReplacesEntries(t *testing.T) {
	m := New(Config{})
	m.AppendUser("本轮新消息")
	input := []Entry{
		{Kind: EntryUser, Text: "旧问题"},
		{Kind: EntryAssistant, Text: "旧回答"},
		{Kind: EntryReasoning, Text: "旧思考"},
		{Kind: EntryTool, Name: "read", Output: "内容"},
	}
	m.SetTranscript(input)
	input[0].Text = "被外部改动" // 验证复制语义

	got := m.Transcript()
	if len(got) != 4 {
		t.Fatalf("transcript = %+v, want 4 entries", got)
	}
	if got[0].Text != "旧问题" || got[1].Text != "旧回答" || got[2].Kind != EntryReasoning {
		t.Fatalf("history wrong: %+v", got)
	}
	for _, e := range got {
		if e.Text == "本轮新消息" || e.Text == "被外部改动" {
			t.Fatalf("old entry / external mutation leaked: %+v", e)
		}
	}
	frame := frameText(m, 100, 24)
	if !strings.Contains(frame, "旧问题") || !strings.Contains(frame, "read") {
		t.Fatalf("history not visible in frame: %q", frame)
	}
}
