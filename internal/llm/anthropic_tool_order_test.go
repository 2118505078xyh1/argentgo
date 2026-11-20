package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestAnthropicToolCallsFollowBlockStartOrder covers gateways that finish
// parallel tool_use blocks in a different order than they announced them. The
// call list must stay in content_block_start (announcement) order, not
// content_block_stop order: a swapped batch is indistinguishable from a
// provider that reordered its tools, and the agent keys results by position in
// some paths.
func TestAnthropicToolCallsFollowBlockStartOrder(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{
			name: "reversed stop notifications",
			script: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_A","name":"read","input":{}}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_B","name":"write","input":{}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"b\":2}"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_stop"}

`,
		},
		{
			name: "one block stops early, the other is flushed at message_stop",
			script: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_A","name":"read","input":{}}}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_B","name":"write","input":{}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"b\":2}"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"message_stop"}

`,
		},
	}

	want := []llm.ToolCall{
		{ID: "toolu_A", Type: "function", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"a":1}`}},
		{ID: "toolu_B", Type: "function", Function: llm.ToolCallFunction{Name: "write", Arguments: `{"b":2}`}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			reply, err := llm.NewAnthropic(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if !reflect.DeepEqual(reply.ToolCalls, want) {
				t.Fatalf("tool calls = %+v, want announcement order %+v", reply.ToolCalls, want)
			}
		})
	}
}
