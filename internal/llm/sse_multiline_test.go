package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestClientsDecodeSSEJSONSplitAcrossDataLines 覆盖这样的网关：它把单个 SSE 事件的
// JSON 载荷包装到多个 data: 行。共享的 SSE
// 读取器按规范用换行符连接这些行；当拆分落在 JSON
// 字符串内部时，连接后的载荷在移除合成换行符之前是无效 JSON。
// chat 客户端一直容忍这一点；Anthropic 和
// Responses 客户端也必须如此。
//
// 第二种形态是网关把多个独立事件作为相邻的
// data: 行发出且没有空行分隔：每行必须按顺序解码。
func TestClientsDecodeSSEJSONSplitAcrossDataLines(t *testing.T) {
	cases := []struct {
		name        string
		build       func(string) llm.Chatter
		script      string
		wantContent string
	}{
		{
			name:  "chat completions split inside string",
			build: func(url string) llm.Chatter { return llm.New(url, "") },
			script: `data: {"choices":[{"delta":{"content":"Hel
data: lo"}}]}

data: [DONE]

`,
			wantContent: "Hello",
		},
		{
			name:  "chat completions adjacent events",
			build: func(url string) llm.Chatter { return llm.New(url, "") },
			script: `data: {"choices":[{"delta":{"content":"Hel"}}]}
data: {"choices":[{"delta":{"content":"lo"}}]}
data: [DONE]

`,
			wantContent: "Hello",
		},
		{
			name:  "anthropic messages split inside string",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			script: `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel
data: lo"}}

data: {"type":"message_stop"}

`,
			wantContent: "Hello",
		},
		{
			name:  "anthropic messages adjacent events",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			script: `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}
data: {"type":"message_stop"}

`,
			wantContent: "Hello",
		},
		{
			name:  "responses split inside string",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			script: `data: {"type":"response.output_text.delta","delta":"Hel
data: lo"}

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			wantContent: "Hello",
		},
		{
			name:  "responses adjacent events",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			script: `data: {"type":"response.output_text.delta","delta":"Hel"}
data: {"type":"response.output_text.delta","delta":"lo"}
data: {"type":"response.completed","response":{"status":"completed"}}

`,
			wantContent: "Hello",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			reply, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if reply.Role != "assistant" || reply.Content != tc.wantContent {
				t.Fatalf("reply = %+v, want assistant/%q", reply, tc.wantContent)
			}
		})
	}
}
