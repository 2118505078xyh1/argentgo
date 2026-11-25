package agent

import (
	"context"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestAssistantRecordCarriesReasoning 锁定推理回放链的第 3 步：当提供方回复携带推理时，通过注入的 Recorder 写入的助手记录
// 是 Reasoning 完整的原始 llm.Message，且内存中的记录也保留它（使后续轮次能回放）。
// （接上行）
// （接上行）
func TestAssistantRecordCarriesReasoning(t *testing.T) {
	const reasoning = "逐步思考：先读文件，再修改，最后给出答案。"
	chat := &fakeChatter{fn: func(n int, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		return llm.Message{Role: "assistant", Content: "done", Reasoning: reasoning}, nil
	}}

	type row struct {
		kind    string
		payload any
	}
	var rows []row
	rec := RecorderFunc(func(kind string, payload any) error {
		rows = append(rows, row{kind: kind, payload: payload})
		return nil
	})
	runner := &Runner{Opts: Options{Model: "m", Chatter: chat, Record: rec}}
	result, err := runner.Run(context.Background(), "task")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	found := false
	for _, r := range rows {
		if r.kind != "assistant" {
			continue
		}
		msg, ok := r.payload.(llm.Message)
		if !ok {
			t.Fatalf("assistant payload = %T, want llm.Message", r.payload)
		}
		if msg.Reasoning == reasoning {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no assistant record carried reasoning %q; rows = %+v", reasoning, rows)
	}

	// 返回给调用方的记录视图也保留推理，这正是选择启用的线上回放会在下一轮次读取的内容。
	// （接上行）
	transcriptHasReasoning := false
	for _, m := range result.Messages {
		if m.Role == "assistant" && m.Reasoning == reasoning {
			transcriptHasReasoning = true
			break
		}
	}
	if !transcriptHasReasoning {
		t.Fatalf("result transcript lost reasoning: %+v", result.Messages)
	}
}

// TestReasoningSurvivesToolPairingView 验证每次请求之前使用的外发视图不会从助手的工具使用轮次中剥离推理。
// （接上行）
func TestReasoningSurvivesToolPairingView(t *testing.T) {
	const reasoning = "中文推理内容"
	messages := []llm.Message{
		{Role: "user", Content: "task"},
		{
			Role:      "assistant",
			ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{}`}}},
			Reasoning: reasoning,
		},
		{Role: "tool", ToolCallID: "c1", Content: "result"},
	}
	view := NormalizeToolPairing(messages)
	if len(view) != 3 {
		t.Fatalf("view = %d messages, want 3", len(view))
	}
	if view[1].Reasoning != reasoning {
		t.Fatalf("view reasoning = %q, want %q", view[1].Reasoning, reasoning)
	}
}
