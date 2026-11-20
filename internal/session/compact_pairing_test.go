package session

import (
	"encoding/json"
	"testing"
)

// assistantWithCalls 构建 assistant Entry，其 Data 携带 agent 循环持久化的
// 完整 llm.Message 载荷形态："tool_calls" 是一个
// 带 "id" 的对象数组。
func assistantWithCalls(content string, ids ...string) Entry {
	calls := make([]any, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      "read",
				"arguments": `{}`,
			},
		})
	}
	data, _ := json.Marshal(map[string]any{
		"role":       "assistant",
		"content":    content,
		"tool_calls": calls,
	})
	return Entry{Role: "assistant", Content: content, Data: data}
}

// toolResult 构建工具 Entry，其结果 id 记录在与转录相同的
// 键（"id"）下，messageEntry 会把它映射为 ToolCallID。
func toolResult(id, content string) Entry {
	data, _ := json.Marshal(map[string]any{"id": id, "name": "read", "output": content})
	return Entry{Role: "tool", Content: content, ToolCallID: id, Data: data}
}

func TestAdjustCutKeepsEveryToolPairOnOneSide(t *testing.T) {
	assistantA1 := assistantWithCalls("a1", "c1")
	assistantA2 := assistantWithCalls("a2", "c2")

	cases := []struct {
		name    string
		entries []Entry
		start   int
		want    int
	}{
		{
			name: "single call and result backed up to assistant",
			entries: []Entry{
				{Role: "system", Content: "s"},
				{Role: "user", Content: "u"},
				assistantWithCalls("call", "c1"),
				toolResult("c1", "t1"),
				{Role: "assistant", Content: "done"},
			},
			start: 3,
			want:  2,
		},
		{
			name: "multi-call batch with all results backed up to one assistant",
			entries: []Entry{
				{Role: "user", Content: "u"},
				assistantWithCalls("batch", "c1", "c2", "c3"),
				toolResult("c1", "t1"),
				toolResult("c2", "t2"),
				toolResult("c3", "t3"),
				{Role: "assistant", Content: "done"},
			},
			start: 4, // 指向第三个结果
			want:  1, // 整批 + assistant 移到保留侧
		},
		{
			name: "interleaved results move back over the issuing assistant",
			entries: []Entry{
				{Role: "user", Content: "u"},
				assistantWithCalls("batch", "c1", "c2"),
				toolResult("c1", "t1"),
				{Role: "user", Content: "intervening steer"},
				toolResult("c2", "t2"),
				{Role: "assistant", Content: "done"},
			},
			start: 4, // 指向第二个结果；两个调用都在 1
			want:  1, // 也回退跳过第一个调用，使任何配对都不被拆分
		},
		{
			name: "cut that strands a call moves back over an intervening user message",
			// 旧版辅助函数只看末尾连续的工具结果。
			// 这里 start 指向一条用户消息，因此旧切分会留下
			// assistant 的调用在摘要中，却保留它的结果。
			entries: []Entry{
				{Role: "user", Content: "u"},
				assistantA1,
				{Role: "user", Content: "follow-up"},
				toolResult("c1", "t1"),
				{Role: "assistant", Content: "done"},
			},
			start: 2,
			want:  1,
		},
		{
			name: "cut after a fully paired batch is unchanged",
			entries: []Entry{
				{Role: "user", Content: "u"},
				assistantA2,
				toolResult("c2", "t2"),
				{Role: "assistant", Content: "done"},
			},
			start: 3,
			want:  3,
		},
		{
			name: "non-tool records are unaffected",
			entries: []Entry{
				{Role: "user", Content: "u1"},
				{Role: "assistant", Content: "a1"},
				{Role: "user", Content: "u2"},
				{Role: "assistant", Content: "a2"},
			},
			start: 2,
			want:  2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := adjustCut(tc.entries, tc.start); got != tc.want {
				t.Errorf("adjustCut(%d) = %d, want %d", tc.start, got, tc.want)
			}
		})
	}
}
