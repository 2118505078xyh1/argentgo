package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestResponsesOutputItemDoneRestoresArguments 覆盖 response.output_item.done：
// 该事件携带完整的 function_call 项，其 arguments 是本次调用参数的权威值。
// 之前的客户端仅在增量为空时才采用它，因此当网关发送的增量被截断、
// 或 output_item.added 里只有部分参数时，工具会以 "{}" 这类错误参数被派发。
func TestResponsesOutputItemDoneRestoresArguments(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   []llm.ToolCall
	}{
		{
			name: "不完整的增量被 output_item.done 的完整参数覆盖",
			script: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"a"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"a.rs\"}"}}

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			want: []llm.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
		},
		{
			name: "added 里的部分参数被 done 的完整参数覆盖",
			script: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_2","name":"write","arguments":"{\"path\":"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_2","name":"write","arguments":"{\"path\":\"b.rs\"}"}}

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			want: []llm.ToolCall{{
				ID:       "call_2",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"b.rs"}`},
			}},
		},
		{
			name: "done 未携带参数时保留完整增量",
			script: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_3","name":"read","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"c.rs\"}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_3","name":"read","arguments":""}}

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			want: []llm.ToolCall{{
				ID:       "call_3",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"c.rs"}`},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			reply, err := llm.NewResponses(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if !reflect.DeepEqual(reply.ToolCalls, tc.want) {
				t.Fatalf("tool calls = %+v, want %+v", reply.ToolCalls, tc.want)
			}
		})
	}
}
