package agent

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// cloneIdemMessages returns a deep copy so metadata mutations in a test cannot
// leak back into the original prefix.
func cloneIdemMessages(in []llm.Message) []llm.Message {
	out := make([]llm.Message, len(in))
	copy(out, in)
	for i := range out {
		if in[i].ToolCalls != nil {
			out[i].ToolCalls = append([]llm.ToolCall(nil), in[i].ToolCalls...)
		}
	}
	return out
}

func idemBaseMessages() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "be concise"},
		{Role: "user", Content: "fix the bug"},
		{Role: "assistant", Content: "ok", ToolCalls: []llm.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "read_file",
				Arguments: `{"path":"a.go"}`,
			},
		}}},
		{Role: "tool", ToolCallID: "call-1", Content: "file body"},
	}
}

func TestSourcePrefixHashIgnoresRoundTripMetadata(t *testing.T) {
	base := idemBaseMessages()
	noisy := cloneIdemMessages(base)

	// llm.Message has no literal id/ts fields; the per-round-trip volatile
	// metadata are these transcript/wire fields plus provider-issued ids.
	noisy[0].CacheControl = &llm.CacheControl{Type: "ephemeral"}
	noisy[0].Usage = &llm.Usage{PromptTokens: 900, CompletionTokens: 1}
	noisy[1].CacheControl = &llm.CacheControl{Type: "ephemeral", TTL: "1h"}
	noisy[2].Reasoning = "let me think about this"
	noisy[2].Usage = &llm.Usage{PromptTokens: 42, CompletionTokens: 7, CachedTokens: 30, CacheWriteTokens: 2}
	noisy[2].FinishReason = "tool_calls"
	noisy[2].CacheControl = &llm.CacheControl{Type: "ephemeral"}
	noisy[2].ToolCalls[0].ID = "call-different-every-round"
	noisy[3].ToolCallID = "call-different-every-round"
	noisy[3].FinishReason = "stop"

	want := SourcePrefixHash(base)
	got := SourcePrefixHash(noisy)
	if got != want {
		t.Fatalf("metadata-only changes altered hash: base=%s noisy=%s", want, got)
	}
	if len(want) != 64 {
		t.Fatalf("hash length = %d, want 64 hex chars", len(want))
	}
	for _, r := range want {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("hash contains non-hex char %q: %s", r, want)
		}
	}
}

func TestSourcePrefixHashChangesWithContent(t *testing.T) {
	base := idemBaseMessages()

	cases := []struct {
		name   string
		mutate func([]llm.Message) []llm.Message
	}{
		{
			name: "user text",
			mutate: func(m []llm.Message) []llm.Message {
				m[1].Content = "fix the other bug"
				return m
			},
		},
		{
			name: "system text",
			mutate: func(m []llm.Message) []llm.Message {
				m[0].Content = "be verbose"
				return m
			},
		},
		{
			name: "tool arguments",
			mutate: func(m []llm.Message) []llm.Message {
				m[2].ToolCalls[0].Function.Arguments = `{"path":"b.go"}`
				return m
			},
		},
		{
			name: "tool name",
			mutate: func(m []llm.Message) []llm.Message {
				m[2].ToolCalls[0].Function.Name = "write_file"
				return m
			},
		},
		{
			name: "role",
			mutate: func(m []llm.Message) []llm.Message {
				m[3].Role = "assistant"
				return m
			},
		},
		{
			name: "extra message",
			mutate: func(m []llm.Message) []llm.Message {
				return append(m, llm.Message{Role: "user", Content: "thanks"})
			},
		},
	}
	want := SourcePrefixHash(base)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := tc.mutate(cloneIdemMessages(base))
			if got := SourcePrefixHash(changed); got == want {
				t.Fatalf("content change did not alter hash: %s", got)
			}
		})
	}
}

func TestSourcePrefixHashTrimsUserWhitespace(t *testing.T) {
	padded := []llm.Message{{Role: "user", Content: "  \n hello world \t "}}
	trimmed := []llm.Message{{Role: "user", Content: "hello world"}}
	if SourcePrefixHash(padded) != SourcePrefixHash(trimmed) {
		t.Fatal("surrounding whitespace on user text changed the hash")
	}

	// Only basic trimming: internal whitespace stays meaningful.
	internal := []llm.Message{{Role: "user", Content: "hello  world"}}
	if SourcePrefixHash(internal) == SourcePrefixHash(trimmed) {
		t.Fatal("internal whitespace should not be collapsed")
	}

	// Non-user roles are left byte-for-byte as-is.
	paddedAssistant := []llm.Message{{Role: "assistant", Content: "  answer  "}}
	trimmedAssistant := []llm.Message{{Role: "assistant", Content: "answer"}}
	if SourcePrefixHash(paddedAssistant) == SourcePrefixHash(trimmedAssistant) {
		t.Fatal("assistant content should not be trimmed")
	}
}

func TestSummaryCacheHitMissAndEviction(t *testing.T) {
	cache := NewSummaryCache(2)

	if got, ok := cache.Get("missing"); ok || got != "" {
		t.Fatalf("miss returned (%q, %v), want (\"\", false)", got, ok)
	}

	cache.Put("a", "summary-a")
	cache.Put("b", "summary-b")
	if got, ok := cache.Get("a"); !ok || got != "summary-a" {
		t.Fatalf("Get(a) = (%q, %v)", got, ok)
	}
	if got, ok := cache.Get("b"); !ok || got != "summary-b" {
		t.Fatalf("Get(b) = (%q, %v)", got, ok)
	}

	// Updating an existing key keeps its insertion position (b stays older).
	cache.Put("b", "summary-b2")
	cache.Put("c", "summary-c") // capacity reached: oldest ("a") is evicted.
	if _, ok := cache.Get("a"); ok {
		t.Fatal("oldest entry was not evicted at capacity")
	}
	if got, ok := cache.Get("b"); !ok || got != "summary-b2" {
		t.Fatalf("updated entry lost or stale: (%q, %v)", got, ok)
	}
	if got, ok := cache.Get("c"); !ok || got != "summary-c" {
		t.Fatalf("Get(c) = (%q, %v)", got, ok)
	}

	// Only finished (non-empty) summaries are cached.
	cache.Put("half-built", "")
	if _, ok := cache.Get("half-built"); ok {
		t.Fatal("empty summary must not be cached")
	}

	// The zero value is usable and falls back to the default capacity.
	var zero SummaryCache
	zero.Put("k", "v")
	if got, ok := zero.Get("k"); !ok || got != "v" {
		t.Fatalf("zero-value cache Get = (%q, %v)", got, ok)
	}
}

func TestSummaryCacheConcurrentUse(t *testing.T) {
	const workers = 32
	const perWorker = 40
	cache := NewSummaryCache(workers*perWorker + 1) // no eviction: every key must survive

	errs := make(chan string, workers*perWorker)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				key := fmt.Sprintf("w%d-i%d", worker, i)
				want := "summary-" + key
				cache.Put(key, want)
				got, ok := cache.Get(key)
				if !ok || got != want {
					errs <- fmt.Sprintf("key %s: got (%q, %v), want (%q, true)", key, got, ok, want)
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestSourcePrefixHashUTF8Stable(t *testing.T) {
	prefix := []llm.Message{{Role: "user", Content: "请总结以下中文内容：你好，世界！🚀"}}
	want := SourcePrefixHash(prefix)
	for i := 0; i < 32; i++ {
		if got := SourcePrefixHash(prefix); got != want {
			t.Fatalf("hash not stable across calls: %s != %s", got, want)
		}
	}

	other := []llm.Message{{Role: "user", Content: "请总结以下中文内容：再见，世界！🚀"}}
	if got := SourcePrefixHash(other); got == want {
		t.Fatal("different UTF-8 content produced the same hash")
	}

	// Chinese text with only surrounding whitespace is still the same prefix.
	padded := []llm.Message{{Role: "user", Content: "  请总结以下中文内容：你好，世界！🚀\n"}}
	if got := SourcePrefixHash(padded); got != want {
		t.Fatalf("padded UTF-8 user text changed hash: %s != %s", got, want)
	}
}
