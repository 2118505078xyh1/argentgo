package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestGeneratedToolCallIDsDoNotCollideAcrossProtocols 覆盖这样的网关：它
// 省略某些工具调用的 id，却在另一个调用上提供等于客户端会从流索引
// 生成的回退名称的提供方 id。重复的调用 id 会让 agent 拒绝整个
// 工具批次，因此每个客户端都必须针对回复中每个提供方 id
// 生成回退值，而不只是针对
// 它正在组装的调用的位置。
func TestGeneratedToolCallIDsDoNotCollideAcrossProtocols(t *testing.T) {
	cases := []struct {
		name  string
		build func(string) llm.Chatter
		// wantProviderID 是提供方提供的 id；生成的 id
		// 必须与它以及彼此都不同。
		wantProviderID string
		script         string
	}{
		{
			name:  "chat completions",
			build: func(url string) llm.Chatter { return llm.New(url, "") },
			// 第一个调用没有 id；第二个实际名为 call_1。
			wantProviderID: "call_1",
			script: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"a","arguments":"{}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_1","function":{"name":"b","arguments":"{}"}}]}}]}

data: [DONE]

`,
		},
		{
			name:  "anthropic messages",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			// 索引 0 的块没有 id；索引 1 的块是 toolu_0。
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
			// output_index 0 的项没有 call_id；output_index 1 是 call_1。
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

// TestGeneratedToolCallIDsSkipEveryProviderID 检查回退生成器中的循环：
// 当某个提供方 id 占据第一个候选时，生成的 id 必须跳过它，
// 而不是停在第一个看起来空闲的名称上。
func TestGeneratedToolCallIDsSkipEveryProviderID(t *testing.T) {
	// 索引 0 没有 id，索引 1 是 toolu_0，索引 2 没有 id。两个
	// 生成的 id 必须与 toolu_0 以及彼此都不同。
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
