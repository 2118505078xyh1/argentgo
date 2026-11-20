package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// message_delta carries the final cache counts. They must be reflected in
// PromptTokens (the complete prompt cost), never double-counted with a cache
// value from message_start, and CachedTokens must stay within PromptTokens.
func TestAnthropicDeltaCacheTokensAreCountedInPrompt(t *testing.T) {
	tests := []struct {
		name           string
		script         string
		wantPrompt     int
		wantCompletion int
		wantCached     int
		wantWrite      int
		wantFresh      int
		wantTotal      int
	}{
		{
			name: "cache tokens only in message_delta",
			script: `data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}

data: {"type":"message_delta","usage":{"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}

data: {"type":"message_stop"}

`,
			wantPrompt:     130,
			wantCompletion: 5,
			wantCached:     100,
			wantWrite:      20,
			wantFresh:      30,
			wantTotal:      135,
		},
		{
			name: "cache tokens only in message_start do not regress",
			script: `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":1}}}

data: {"type":"message_delta","usage":{"output_tokens":4}}

data: {"type":"message_stop"}

`,
			wantPrompt:     60,
			wantCompletion: 4,
			wantCached:     30,
			wantWrite:      20,
			wantFresh:      30,
			wantTotal:      64,
		},
		{
			name: "message_delta cache values replace message_start values",
			script: `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_creation_input_tokens":5,"cache_read_input_tokens":30,"output_tokens":1}}}

data: {"type":"message_delta","usage":{"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}

data: {"type":"message_stop"}

`,
			wantPrompt:     130,
			wantCompletion: 5,
			wantCached:     100,
			wantWrite:      20,
			wantFresh:      30,
			wantTotal:      135,
		},
		{
			name: "message_delta without message_start",
			script: `data: {"type":"message_delta","usage":{"output_tokens":7,"cache_read_input_tokens":40,"cache_creation_input_tokens":2}}

data: {"type":"message_stop"}

`,
			wantPrompt:     42,
			wantCompletion: 7,
			wantCached:     40,
			wantWrite:      2,
			wantFresh:      2,
			wantTotal:      49,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, tt.script)
			}))
			defer server.Close()

			reply, err := llm.NewAnthropic(server.URL, "k").Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if reply.Usage == nil {
				t.Fatal("no usage reported")
			}
			u := reply.Usage
			if u.PromptTokens != tt.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", u.PromptTokens, tt.wantPrompt)
			}
			if u.CompletionTokens != tt.wantCompletion {
				t.Errorf("CompletionTokens = %d, want %d", u.CompletionTokens, tt.wantCompletion)
			}
			if u.CachedTokens != tt.wantCached {
				t.Errorf("CachedTokens = %d, want %d", u.CachedTokens, tt.wantCached)
			}
			if u.CacheWriteTokens != tt.wantWrite {
				t.Errorf("CacheWriteTokens = %d, want %d", u.CacheWriteTokens, tt.wantWrite)
			}
			if u.CachedTokens > u.PromptTokens {
				t.Errorf("CachedTokens (%d) exceeds PromptTokens (%d)", u.CachedTokens, u.PromptTokens)
			}
			if got := u.FreshPromptTokens(); got != tt.wantFresh {
				t.Errorf("FreshPromptTokens() = %d, want %d", got, tt.wantFresh)
			}
			if got := u.TotalTokens(); got != tt.wantTotal {
				t.Errorf("TotalTokens() = %d, want %d", got, tt.wantTotal)
			}
		})
	}
}
