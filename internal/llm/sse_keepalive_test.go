package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestProtocolStreamsSkipEmptySSEDataEvents 覆盖这样的网关：它在真实事件之间
// 发送空的 data: 行作为保活。SSE 规范把这种
// 事件视为不带数据；每个客户端都必须忽略它，而不是尝试
// 将其解码为 JSON，并用一个流错误中止原本有效的回复。
// 裸的 "data:" 写法和 "data: "（一个可选空格）都
// 在覆盖范围内，某些代理发出的纯空白载荷也覆盖。
func TestProtocolStreamsSkipEmptySSEDataEvents(t *testing.T) {
	cases := []struct {
		name        string
		build       func(string) llm.Chatter
		script      string
		wantContent string
	}{
		{
			name:  "chat completions",
			build: func(url string) llm.Chatter { return llm.New(url, "") },
			script: `data:

data: {"choices":[{"delta":{"content":"Hel"}}]}

data:  
data: {"choices":[{"delta":{"content":"lo"}}]}

data:

data: [DONE]

`,
			wantContent: "Hello",
		},
		{
			name:  "anthropic messages",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			script: `data:

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

data:  
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

data:

data: {"type":"message_stop"}

`,
			wantContent: "Hello",
		},
		{
			name:  "responses",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			script: `data:

data: {"type":"response.output_text.delta","delta":"Hel"}

data:  
data: {"type":"response.output_text.delta","delta":"lo"}

data:

data: {"type":"response.completed","response":{"status":"completed"}}

`,
			wantContent: "Hello",
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
			if reply.Role != "assistant" || reply.Content != tc.wantContent {
				t.Fatalf("reply = %+v, want assistant/%q", reply, tc.wantContent)
			}
		})
	}
}
