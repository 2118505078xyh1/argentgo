package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestResponsesArgumentsDoneAfterItemDone 覆盖参数完成事件晚于
// output_item.done 到达的网关。function_call_arguments.done 携带的参数
// 是权威值，即使它晚于条目完成事件到达也必须覆盖已组装调用的参数；
// 否则工具会以 item.done 中的旧参数（或 "{}"）被派发。
func TestResponsesArgumentsDoneAfterItemDone(t *testing.T) {
	script := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":""}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_1","name":"read","arguments":"{\"path\":\"old.rs\"}"}}

data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_1","arguments":"{\"path\":\"new.rs\"}"}

data: {"type":"response.completed","response":{"status":"completed"}}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	reply, err := llm.NewResponses(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	want := []llm.ToolCall{{
		ID:       "call_1",
		Type:     "function",
		Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"new.rs"}`},
	}}
	if !reflect.DeepEqual(reply.ToolCalls, want) {
		t.Fatalf("tool calls = %+v, want %+v", reply.ToolCalls, want)
	}
}
