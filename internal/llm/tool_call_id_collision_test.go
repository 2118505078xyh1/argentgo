package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestGeneratedToolCallIDsDoNotCollideAcrossProtocols covers a gateway that
// omits the id on some tool calls while supplying a provider id on another that
// happens to equal the fallback name the client would generate from the stream
// index. A duplicate call id makes the agent reject the whole tool batch, so
// each client must generate its fallbacks against every provider id in the
// reply, not just against the position of the call it is assembling.
func TestGeneratedToolCallIDsDoNotCollideAcrossProtocols(t *testing.T) {
	cases := []struct {
		name  string
		build func(string) llm.Chatter
		// wantProviderID is an id the provider supplied; the generated ids
		// must stay distinct from it and from each other.
		wantProviderID string
		script         string
	}{
		{
			name:  "chat completions",
			build: func(url string) llm.Chatter { return llm.New(url, "") },
			// The first call has no id; the second is really named call_1.
			wantProviderID: "call_1",
			script: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"a","arguments":"{}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_1","function":{"name":"b","arguments":"{}"}}]}}]}

data: [DONE]

`,
		},
		{
			name:  "anthropic messages",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			// The index-0 block has no id; the index-1 block is toolu_0.
			wantProviderID: "toolu_0",
			script: `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"a","input":{}}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_0","name":"b","input":{}}}

data: {"type":"content_block_stop","index":1}

data: {"type":"message_stop"}

`,
		},
		{
			name:  "responses",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			// The output_index-0 item has no call_id; output_index-1 is call_1.
			wantProviderID: "call_1",
			script: `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","name":"a","arguments":"{}"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"a","arguments":"{}"}}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"b","arguments":"{}"}}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"b","arguments":"{}"}}

data: {"type":"response.completed","response":{}}

`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			reply, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if len(reply.ToolCalls) != 2 {
				t.Fatalf("tool calls = %+v, want 2", reply.ToolCalls)
			}
			seen := make(map[string]bool, len(reply.ToolCalls))
			for _, call := range reply.ToolCalls {
				if strings.TrimSpace(call.ID) == "" {
					t.Fatalf("empty tool call id in %+v", reply.ToolCalls)
				}
				if seen[call.ID] {
					t.Fatalf("duplicate tool call id %q in %+v", call.ID, reply.ToolCalls)
				}
				seen[call.ID] = true
			}
			if !seen[tc.wantProviderID] {
				t.Fatalf("provider id %q missing from %+v", tc.wantProviderID, reply.ToolCalls)
			}
		})
	}
}

// TestGeneratedToolCallIDsSkipEveryProviderID checks the loop in the fallback
// generator: when a provider id occupies the first candidate, a generated id
// must move past it rather than stop at the first free-looking name.
func TestGeneratedToolCallIDsSkipEveryProviderID(t *testing.T) {
	// Index 0 has no id, index 1 is toolu_0, index 2 has no id. The two
	// generated ids must be distinct from toolu_0 and from each other.
	script := `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","name":"a","input":{}}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_0","name":"b","input":{}}}

data: {"type":"content_block_stop","index":1}

data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","name":"c","input":{}}}

data: {"type":"content_block_stop","index":2}

data: {"type":"message_stop"}

`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, script)
	}))
	defer server.Close()

	reply, err := llm.NewAnthropic(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(reply.ToolCalls) != 3 {
		t.Fatalf("tool calls = %+v, want 3", reply.ToolCalls)
	}
	seen := make(map[string]bool, len(reply.ToolCalls))
	for _, call := range reply.ToolCalls {
		if seen[call.ID] {
			t.Fatalf("duplicate tool call id %q in %+v", call.ID, reply.ToolCalls)
		}
		seen[call.ID] = true
	}
	if !seen["toolu_0"] {
		t.Fatalf("provider id toolu_0 missing from %+v", reply.ToolCalls)
	}
}
