package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestChatClientToolCallIDAliases 覆盖在流中途重新编号工具调用
// 或交换其 id 的网关。每个片段都必须落到以第一个提供方 id 标识的
// 单一调用中；否则 agent 会看到 id 重复的假调用，
// 并拒绝整个批次。
func TestChatClientToolCallIDAliases(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   []llm.ToolCall
	}{
		{
			name: "index omitted on continuation resolves by id",
			script: `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_A","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"id":"call_A","function":{"arguments":"\"a.rs\"}"}}]}}]}

data: [DONE]

`,
			want: []llm.ToolCall{{
				ID:       "call_A",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
		},
		{
			name: "index changes but id resolves to the same call",
			script: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_A","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_A","function":{"arguments":"\"a.rs\"}"}}]}}]}

data: [DONE]

`,
			want: []llm.ToolCall{{
				ID:       "call_A",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
		},
		{
			name: "id rename is an alias and keeps the first id",
			script: `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_A","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_B","function":{"arguments":"\"a.rs\""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"id":"call_B","function":{"arguments":"}"}}]}}]}

data: [DONE]

`,
			want: []llm.ToolCall{{
				ID:       "call_A",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			reply, err := llm.New(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if !reflect.DeepEqual(reply.ToolCalls, tc.want) {
				t.Fatalf("tool calls = %+v, want %+v", reply.ToolCalls, tc.want)
			}
		})
	}
}
