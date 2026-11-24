package tui

import (
	"strings"
	"testing"
)

// TestReasoningEventProducesTranscriptBlock 守护推理事件到 transcript 的"最后一公里"：
// agent 的 reasoning_delta 经桥接映射为 EventReasoning，HandleEvent 必须产出推理条目，
// 并能在帧里看到推理标记。这个链路曾整段缺失（AppendReasoning 零生产调用）。
func TestReasoningEventProducesTranscriptBlock(t *testing.T) {
	m := New(Config{Model: "model-x"})
	m.HandleEvent(Event{Kind: EventReasoning, Reasoning: "先读文件再修改"})
	entries := m.Transcript()
	if len(entries) != 1 || entries[0].Kind != EntryReasoning {
		t.Fatalf("entries = %+v, want one reasoning entry", entries)
	}
	if entries[0].Text != "先读文件再修改" {
		t.Fatalf("reasoning text = %q", entries[0].Text)
	}
	frame := frameText(m, 100, 24)
	if !strings.Contains(frame, "思考") && !strings.Contains(frame, "◇") {
		t.Fatalf("reasoning block not visible in frame: %q", frame)
	}
}

// TestToolEventsProduceLiveThenStaticCard 守护工具调用在正文可见：
// tool_start 产生运行中卡片（spinner），tool_end 把它切成静态卡片并填入输出。
func TestToolEventsProduceLiveThenStaticCard(t *testing.T) {
	m := New(Config{Model: "model-x"})
	m.HandleEvent(Event{Kind: EventToolStart, ToolCallID: "c1", ToolName: "read", Args: `{"path":"a.go"}`})
	entries := m.Transcript()
	if len(entries) != 1 || entries[0].Kind != EntryTool || !entries[0].Running {
		t.Fatalf("after start entries = %+v, want one running tool entry", entries)
	}
	m.HandleEvent(Event{Kind: EventToolEnd, ToolCallID: "c1", ToolName: "read", Output: "file content", DurationMS: 12})
	entries = m.Transcript()
	if len(entries) != 1 || entries[0].Running {
		t.Fatalf("after end entries = %+v, want one static tool entry", entries)
	}
	if !strings.Contains(entries[0].Output, "file content") {
		t.Fatalf("tool output missing: %+v", entries[0])
	}
	frame := frameText(m, 100, 24)
	if !strings.Contains(frame, "read") {
		t.Fatalf("tool name not visible in frame: %q", frame)
	}
}

// TestEmptyReasoningDoesNotCreateBlock 空推理不应产生空块（噪声）。
func TestEmptyReasoningDoesNotCreateBlock(t *testing.T) {
	m := New(Config{})
	m.HandleEvent(Event{Kind: EventReasoning, Reasoning: "   \n"})
	if got := len(m.Transcript()); got != 0 {
		t.Fatalf("empty reasoning produced %d entries", got)
	}
}
