package llm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/argentgo/internal/llm"
)

// TestChatUsageEmptyTerminatorDoesNotEraseTotals 覆盖在真实最终用量分块之后
// 追加全零用量对象的网关（通常是尾部无 choice 终止事件的一部分）。
// 这个空快照不得替换已收到的总计，
// 否则所有下游预算/成本计算都会
// 悄悄报告为零。
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

// TestChatUsagePartialSnapshotsAreMerged 覆盖把一次回复的用量拆分到
// 多个分块上报、而非在最后一个分块全部上报的网关。
// 省略某个计数器的分块不得抹掉已经收到的计数：否则会产生这样的 Usage——
// CachedTokens 超过 PromptTokens 且 TotalTokens 为零，
// 从而破坏下游的预算/成本核算。
func TestChatUsagePartialSnapshotsAreMerged(t *testing.T) {
	cases := []struct {
		name           string
		script         string
		wantPrompt     int
		wantCompletion int
		wantCached     int
	}{
		{
			name: "cached breakdown arrives after totals",
			script: `data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}
data: {"choices":[],"usage":{"prompt_tokens_details":{"cached_tokens":4}}}
data: [DONE]

`,
			wantPrompt:     10,
			wantCompletion: 3,
			wantCached:     4,
		},
		{
			name: "prompt only then completion only",
			script: `data: {"choices":[],"usage":{"prompt_tokens":12}}
data: {"choices":[{"delta":{"content":"hi"}}],"usage":{"completion_tokens":5}}
data: [DONE]

`,
			wantPrompt:     12,
			wantCompletion: 5,
		},
		{
			name: "empty terminator between partial snapshots",
			script: `data: {"choices":[],"usage":{"prompt_tokens":9}}
data: {"choices":[],"usage":{}}
data: {"choices":[],"usage":{"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":6}}}
data: [DONE]

`,
			wantPrompt:     9,
			wantCompletion: 2,
			wantCached:     6,
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
			u := reply.Usage
			if u.PromptTokens != tc.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", u.PromptTokens, tc.wantPrompt)
			}
			if u.CompletionTokens != tc.wantCompletion {
				t.Errorf("CompletionTokens = %d, want %d", u.CompletionTokens, tc.wantCompletion)
			}
			if u.CachedTokens != tc.wantCached {
				t.Errorf("CachedTokens = %d, want %d", u.CachedTokens, tc.wantCached)
			}
			if u.CachedTokens > u.PromptTokens {
				t.Errorf("CachedTokens (%d) exceeds PromptTokens (%d)", u.CachedTokens, u.PromptTokens)
			}
			if got, want := u.TotalTokens(), tc.wantPrompt+tc.wantCompletion; got != want {
				t.Errorf("TotalTokens = %d, want %d", got, want)
			}
		})
	}
}

// TestAnthropicMessageDeltaZeroCacheResetsMessageStart 覆盖这样的网关：
// 它在 message_start 报告缓存计数，随后在 message_delta 中给出权威的零计数
// （最终核算快照上的缓存未命中）。显式的零必须清除陈旧的 message_start 值；
// 省略的字段必须保留它们。弄错这一点会破坏
// CachedTokens 和 FreshPromptTokens。
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
