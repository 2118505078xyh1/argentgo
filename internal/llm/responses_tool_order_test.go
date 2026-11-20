package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestResponsesToolCallsFollowAddedOrder covers a gateway that emits the
// output_item.done events for parallel function calls in a different order
// than their output_item.added events. The call list must stay in the order the
// items were announced (output_index order), not done-notification order:
// the agent loop associates each result with a call by position in some paths,
// and a swapped batch is indistinguishable from a provider that reordered its
// tools. The order slice already records the announcement order, but calls are
// appended eagerly on done, which silently loses it.
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
