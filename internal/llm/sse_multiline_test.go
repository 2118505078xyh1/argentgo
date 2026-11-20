package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestClientsDecodeSSEJSONSplitAcrossDataLines covers a gateway that wraps a
// single SSE event's JSON payload across several data: lines. The shared SSE
// reader joins those lines with newlines; when the split falls inside a JSON
// string the joined payload is invalid JSON until the synthetic newlines are
// removed. The chat client has always tolerated this; the Anthropic and
// Responses clients must as well.
//
// A second shape is a gateway that emits several independent events as adjacent
// data: lines with no blank separator: each line must be decoded in order.
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
