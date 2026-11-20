package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestChatClientToolCallIDWinsWhenIndexIsReassigned 覆盖在流中途重新编号
// 工具调用的网关：一个较晚的增量携带已知的调用 id，但其索引
// 原本属于另一个调用。id 是稳定的标识，因此片段必须跟随 id，
// 而不是陈旧的索引。
func TestChatClientToolCallIDWinsWhenIndexIsReassigned(t *testing.T) {
	script := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_A","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_B","function":{"name":"write","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_B","function":{"arguments":"\"b.rs\"}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_A","function":{"arguments":"\"a.rs\"}"}}]}}]}

data: [DONE]

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	want := []llm.ToolCall{
		{ID: "call_A", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`}},
		{ID: "call_B", Type: "function", Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"b.rs"}`}},
	}
	reply, err := llm.New(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !reflect.DeepEqual(reply.ToolCalls, want) {
		t.Fatalf("tool calls = %+v, want %+v", reply.ToolCalls, want)
	}
}
