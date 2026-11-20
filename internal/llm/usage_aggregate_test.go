package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestChatUsageEmptyTerminatorDoesNotEraseTotals covers a gateway that appends
// an all-zero usage object after the real final usage chunk (often as part of
// a trailing, choice-less terminator event). The empty snapshot must not
// replace the totals already received, or every downstream budget/cost
// calculation silently reports zero.
func TestChatUsageEmptyTerminatorDoesNotEraseTotals(t *testing.T) {
	cases := []struct {
		name           string
		script         string
		wantPrompt     int
		wantCompletion int
		wantCached     int
	}{
		{
			name: "empty usage object after the final usage chunk",
			script: `data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}}
data: {"choices":[],"usage":{}}
data: [DONE]

`,
			wantPrompt:     10,
			wantCompletion: 3,
			wantCached:     4,
		},
		{
			name: "zero snapshot before the real usage still adopts the real one",
			script: `data: {"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}
data: {"choices":[],"usage":{"prompt_tokens":25,"completion_tokens":7}}
data: [DONE]

`,
			wantPrompt:     25,
			wantCompletion: 7,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tc.script)
			}))
			defer server.Close()

			reply, err := llm.New(server.URL, "").Chat(context.Background(), "m", nil, nil, nil)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if reply.Usage == nil {
				t.Fatal("no usage reported")
			}
			if reply.Usage.PromptTokens != tc.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", reply.Usage.PromptTokens, tc.wantPrompt)
			}
			if reply.Usage.CompletionTokens != tc.wantCompletion {
				t.Errorf("CompletionTokens = %d, want %d", reply.Usage.CompletionTokens, tc.wantCompletion)
			}
			if reply.Usage.CachedTokens != tc.wantCached {
				t.Errorf("CachedTokens = %d, want %d", reply.Usage.CachedTokens, tc.wantCached)
			}
		})
	}
}

// TestAnthropicMessageDeltaZeroCacheResetsMessageStart covers a gateway that
// reports cache counts in message_start and then authoritative zero counts in
// message_delta (a cache miss on the final accounting snapshot). An explicit
// zero must clear the stale message_start values; an omitted field must keep
// them. Getting this wrong corrupts CachedTokens and FreshPromptTokens.
func TestAnthropicMessageDeltaZeroCacheResetsMessageStart(t *testing.T) {
	cases := []struct {
		name       string
		deltaUsage string
		wantPrompt int
		wantCached int
		wantWrite  int
		wantFresh  int
	}{
		{
			name:       "explicit zero cache counts clear message_start values",
			deltaUsage: `"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0`,
			wantPrompt: 10,
			wantCached: 0,
			wantWrite:  0,
			wantFresh:  10,
		},
		{
			name:       "omitted cache fields keep message_start values",
			deltaUsage: `"output_tokens":5`,
			wantPrompt: 60,
			wantCached: 30,
			wantWrite:  20,
			wantFresh:  30,
		},
		{
			name:       "new non-zero counts replace message_start values",
			deltaUsage: `"output_tokens":5,"cache_read_input_tokens":40,"cache_creation_input_tokens":10`,
			wantPrompt: 60,
			wantCached: 40,
			wantWrite:  10,
			wantFresh:  20,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := `data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":30,"cache_creation_input_tokens":20}}}
data: {"type":"message_delta","usage":{` + tc.deltaUsage + `}}
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
			if reply.Usage == nil {
				t.Fatal("no usage reported")
			}
			u := reply.Usage
			if u.PromptTokens != tc.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", u.PromptTokens, tc.wantPrompt)
			}
			if u.CachedTokens != tc.wantCached {
				t.Errorf("CachedTokens = %d, want %d", u.CachedTokens, tc.wantCached)
			}
			if u.CacheWriteTokens != tc.wantWrite {
				t.Errorf("CacheWriteTokens = %d, want %d", u.CacheWriteTokens, tc.wantWrite)
			}
			if got := u.FreshPromptTokens(); got != tc.wantFresh {
				t.Errorf("FreshPromptTokens() = %d, want %d", got, tc.wantFresh)
			}
		})
	}
}
