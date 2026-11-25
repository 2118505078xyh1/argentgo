package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestResponsesToolCallsFollowAddedOrder 覆盖以与 output_item.added 事件
// 不同的顺序发出并行函数调用的 output_item.done 事件的网关。
// 调用列表必须保持各项被宣布的顺序（output_index 顺序），
// 而非 done 通知顺序：agent 循环在某些路径中按位置把结果与调用关联，
// 而批次被交换与提供方重排了工具无法区分。
// order 切片已记录宣布顺序，但调用在 done 时
// 被急切追加，
// 从而悄悄丢失了该顺序。
func TestResponsesToolCallsFollowAddedOrder(t *testing.T) {
	script := sse(
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_A","name":"read","arguments":""}}`+"\n\n",
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_B","name":"write","arguments":""}}`+"\n\n",
		`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"a\":1}"}`+"\n\n",
		`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_B","name":"write","arguments":"{\"b\":2}"}}`+"\n\n",
		`data: {"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"b\":2}"}`+"\n\n",
		`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_A","name":"read","arguments":"{\"a\":1}"}}`+"\n\n",
		`data: {"type":"response.completed","response":{"status":"completed"}}`+"\n\n",
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	reply, err := llm.NewResponses(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	want := []llm.ToolCall{
		{ID: "call_A", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"a":1}`}},
		{ID: "call_B", Type: "function", Function: llm.ToolCallFunction{Name: "write", Arguments: `{"b":2}`}},
	}
	if !reflect.DeepEqual(reply.ToolCalls, want) {
		t.Fatalf("tool calls = %+v, want announcement order %+v", reply.ToolCalls, want)
	}
}
