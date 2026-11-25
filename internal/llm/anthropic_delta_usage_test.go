package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestAnthropicDeltaInputTokensAreUsed 覆盖 LiteLLM/OpenRouter 等网关使用的
// Anthropic 透传形态：message_start 报告零用量，
// 而最终的 message_delta 携带真实的 input_tokens（完整的提示成本，
// 类似 chat-completions 的 prompt_tokens）。忽略该字段会报告零
// 提示 token，并在无声中破坏所有下游预算/成本计算。
func TestAnthropicDeltaInputTokensAreUsed(t *testing.T) {
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
			name: "delta input tokens with zeroed message_start",
			script: `data: {"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5509,"output_tokens":4}}

data: {"type":"message_stop"}

`,
			wantPrompt:     5509,
			wantCompletion: 4,
			wantCached:     0,
			wantWrite:      0,
			wantFresh:      5509,
			wantTotal:      5513,
		},
		{
			name: "delta total input wins over message_start fresh input",
			script: `data: {"type":"message_start","message":{"usage":{"input_tokens":8,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5509,"output_tokens":4,"cache_read_input_tokens":5501}}

data: {"type":"message_stop"}

`,
			wantPrompt:     5509,
			wantCompletion: 4,
			wantCached:     5501,
			wantWrite:      0,
			wantFresh:      8,
			wantTotal:      5513,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
			if got := u.FreshPromptTokens(); got != tt.wantFresh {
				t.Errorf("FreshPromptTokens() = %d, want %d", got, tt.wantFresh)
			}
			if got := u.TotalTokens(); got != tt.wantTotal {
				t.Errorf("TotalTokens() = %d, want %d", got, tt.wantTotal)
			}
		})
	}
}
