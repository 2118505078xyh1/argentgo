package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestProtocolStreamsSkipEmptySSEDataEvents covers gateways that send an empty
// data: line as a keepalive between real events. The SSE spec treats such an
// event as carrying no data; every client must ignore it rather than try to
// decode it as JSON and abort an otherwise valid reply with a stream error.
// Both the bare "data:" spelling and "data: " (a single optional space) are
// covered, as is the whitespace-only payload some proxies emit.
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
