package session

import (
	"encoding/json"
	"testing"
)

// assistantWithCalls builds an assistant Entry whose Data carries the full
// llm.Message payload shape persisted by the agent loop: "tool_calls" is an
// array of objects with an "id".
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

// toolResult builds a tool Entry with the result id recorded under the same
// key the transcript uses ("id"), which messageEntry maps to ToolCallID.
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
			start: 4, // points at the third result
			want:  1, // whole batch + assistant moved to the kept side
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
			start: 4, // points at the second result; both calls are at 1
			want:  1, // back up over the first call too, so no pair is split
		},
		{
			name: "cut that strands a call moves back over an intervening user message",
			// The legacy helper only looks at a trailing run of tool results.
			// Here start points at a user message, so the old cut would leave
			// assistant's call in the summary while keeping its result.
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
