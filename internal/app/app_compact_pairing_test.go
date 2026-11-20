package app

import (
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// toolCallMessages 是一份记录，若在适配中丢失调用 id，其切点会落在
// 索引 1 的调用与其索引 3 的结果之间。
// 中间的用户消息也会使旧版尾部工具回退失效。
func toolCallMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "assistant", Content: "call", ToolCalls: []llm.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: llm.ToolCallFunction{Name: "patch", Arguments: `{"file_path":"src/a.go","filePath":"src/b.go"}`},
		}}},
		{Role: "user", Content: "steer"},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
		{Role: "assistant", Content: "done"},
	}
}

func TestEntriesFromMessagesCarriesToolCallPayload(t *testing.T) {
	entries := entriesFromMessages(toolCallMessages())
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(entries))
	}

	call := entries[1]
	if call.Role != "assistant" || len(call.Data) == 0 {
		t.Fatalf("assistant entry must carry tool-call data: %+v", call)
	}
	var payload struct {
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(call.Data, &payload); err != nil {
		t.Fatalf("unmarshal tool-call data: %v", err)
	}
	if len(payload.ToolCalls) != 1 || payload.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool_calls ids = %+v, want [call_1]", payload.ToolCalls)
	}
	if !containsString(payload.Files, "src/a.go") || !containsString(payload.Files, "src/b.go") {
		t.Fatalf("normalised files = %+v, want src/a.go and src/b.go", payload.Files)
	}

	if got := entries[3].ToolCallID; got != "call_1" {
		t.Fatalf("tool result ToolCallID = %q, want call_1", got)
	}
}

func TestEntriesFromMessagesPairingSurvivesBudgetCut(t *testing.T) {
	entries := entriesFromMessages(toolCallMessages())
	const callIndex, resultIndex = 1, 3

	sawCut := false
	for budget := 1; budget <= 200; budget++ {
		start := session.CutForBudget(entries, budget)
		if start > 0 {
			sawCut = true
		}
		// 严格位于调用与其结果之间的切点，会摘要该调用，
		// 却原样保留结果，产生一个孤立的、下一次提供方请求会拒绝的
		// 工具结果。
		if start > callIndex && start <= resultIndex {
			t.Fatalf("CutForBudget(%d) = %d splits call %d from result %d", budget, start, callIndex, resultIndex)
		}
	}
	if !sawCut {
		t.Fatal("no budget produced a cut; the pairing assertion was vacuous")
	}
}

func TestEntriesFromMessagesChangedFilesSurviveSummarize(t *testing.T) {
	messages := toolCallMessages()
	entries := entriesFromMessages(messages)

	// 丢弃系统/用户消息以及助手的工具调用，正如一次真实的
	// 轮次中压缩在窗口预算迫使该切点时会做的那样。
	compaction := session.Summarize(entries[:2], len(entries)-2)
	if !containsString(compaction.Files, "src/a.go") || !containsString(compaction.Files, "src/b.go") {
		t.Fatalf("compaction files = %+v, want src/a.go and src/b.go", compaction.Files)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
