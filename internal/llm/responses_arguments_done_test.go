package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestResponsesFunctionCallArgumentsDoneRestoresArguments 覆盖
// response.function_call_arguments.done 事件：它携带一次函数调用最终、完整的参数。
// 之前的客户端完全忽略该事件，只从增量里累积参数。对于不发送增量（或在增量中
// 参数被截断）的网关，这会让工具调用以 "{}" 这种错误参数被派发。
func TestResponsesFunctionCallArgumentsDoneRestoresArguments(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   []llm.ToolCall
	}{
		{
			name: "仅发送参数完成事件，没有增量也没有 output_item.done",
			script: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":""}}

data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","arguments":"{\"path\":\"a.rs\"}"}

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			want: []llm.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.rs"}`},
			}},
		},
		{
			name: "不完整的增量被权威的完整参数覆盖",
			script: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_2","name":"write","arguments":""}}

data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":\"a"}

data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_2","arguments":"{\"path\":\"b.rs\"}"}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_2","name":"write","arguments":""}}

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			want: []llm.ToolCall{{
				ID:       "call_2",
				Type:     "function",
				Function: llm.ToolCallFunction{Name: "write", Arguments: `{"path":"b.rs"}`},
			}},
		},
		{
			name: "只有完成事件的并行调用各取各的参数",
			script: `data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_a","name":"read","arguments":"{\"a\":1}"}

data: {"type":"response.function_call_arguments.done","output_index":1,"item_id":"fc_b","name":"write","arguments":"{\"b\":2}"}

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			want: []llm.ToolCall{
				{ID: "fc_a", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"a":1}`}},
				{ID: "fc_b", Type: "function", Function: llm.ToolCallFunction{Name: "write", Arguments: `{"b":2}`}},
			},
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
