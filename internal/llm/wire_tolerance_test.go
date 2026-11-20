package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestProtocolStreamsTolerateUnknownWireValues 检查每个客户端是否忽略它从未见过的
// 事件类型、status/finish_reason 枚举和额外字段。
// "仍产生终态事件"意味着 Chat 返回了组装好的消息：
// 流到达了它的 [DONE]/message_stop/response.completed 标记。
func TestProtocolStreamsTolerateUnknownWireValues(t *testing.T) {
	cases := []struct {
		name        string
		build       func(string) llm.Chatter
		script      string
		wantContent string
	}{
		{
			name:  "chat completions",
			build: func(url string) llm.Chatter { return llm.New(url, "") },
			script: `data: {"type":"future.event","id":"ev_1","status":"queued_future","future":{"nested":true}}
data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":"future_reason"}],"status":"in_progress_future","extra":1}

data: {"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}],"future_field":[1,2,3]}

data: [DONE]

`,
			wantContent: "Hello world",
		},
		{
			name:  "anthropic messages",
			build: func(url string) llm.Chatter { return llm.NewAnthropic(url, "") },
			script: `event: future.event
data: {"type":"future.event","future_field":1}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""},"stop_reason":"future_stop"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"},"status":"future_status"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"},"extra":{"nested":true}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"future_reason"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop","future_field":"ignored"}

`,
			wantContent: "Hello world",
		},
		{
			name:  "responses",
			build: func(url string) llm.Chatter { return llm.NewResponses(url, "") },
			script: `data: {"type":"response.future_event","delta":"ignored","status":"future_status"}

data: {"type":"response.output_text.delta","delta":"Hello","status":"in_progress_future","extra":{"nested":1}}

data: {"type":"response.output_text.delta","delta":" world","finish_reason":"future_reason","extra":[1,2]}

data: {"type":"response.completed","response":{"status":"completed_future","usage":{"input_tokens":3,"output_tokens":2,"future_field":9}},"future_field":"ignored"}

`,
			wantContent: "Hello world",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			var deltas strings.Builder
			reply, err := tc.build(server.URL).Chat(context.Background(), "m", nil, nil, func(delta string) {
				deltas.WriteString(delta)
			})
			if err != nil {
				t.Fatalf("Chat returned an error for unknown wire values: %v", err)
			}
			if reply.Role != "assistant" || reply.Content != tc.wantContent {
				t.Fatalf("reply = %+v, want assistant/%q", reply, tc.wantContent)
			}
			if deltas.String() != tc.wantContent {
				t.Fatalf("streamed deltas = %q, want %q (terminal event missing?)", deltas.String(), tc.wantContent)
			}
		})
	}
}
