package agent

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"example.com/argentgo/internal/llm"
)

// countingSummaryCompactor 是一个产生摘要的伪实现。它记录真实的（有副作用的）摘要路径
// 运行了多少次，并返回一个简短视图，其系统消息由对话内容派生。
// （接上行）
type countingSummaryCompactor struct {
	calls atomic.Int64
}

func (c *countingSummaryCompactor) MaybeCompact(messages []llm.Message, window int) ([]llm.Message, bool) {
	c.calls.Add(1)
	return []llm.Message{
		{Role: "system", Content: "summary of " + messages[1].Content},
		messages[len(messages)-1],
	}, true
}

func cacheWirePrefix() []llm.Message {
	return []llm.Message{
		{Role: "system", Content: "rules"},
		{Role: "user", Content: "fix the bug"},
		{Role: "assistant", Content: "on it", ToolCalls: []llm.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: "tool", ToolCallID: "call-1", Content: "file body"},
	}
}

func TestCachingCompactorReusesFinishedSummaryForSamePrefix(t *testing.T) {
	inner := &countingSummaryCompactor{}
	c := newCachingCompactor(inner, NewSummaryCache(8))
	prefix := cacheWirePrefix()

	first, changed := c.MaybeCompact(prefix, 100)
	if !changed || len(first) == 0 {
		t.Fatalf("first MaybeCompact = (%v, %v), want a real compacted view", first, changed)
	}
	second, changed := c.MaybeCompact(prefix, 100)
	if !changed || len(second) == 0 {
		t.Fatalf("second MaybeCompact = (%v, %v), want a real compacted view", second, changed)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("summariser called %d times for one prefix, want 1", got)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cache hit replayed a different view:\nfirst  = %#v\nsecond = %#v", first, second)
	}
}

func TestCachingCompactorIgnoresRoundTripMetadataOnCacheHit(t *testing.T) {
	inner := &countingSummaryCompactor{}
	c := newCachingCompactor(inner, NewSummaryCache(8))
	prefix := cacheWirePrefix()

	if _, ok := c.MaybeCompact(prefix, 100); !ok {
		t.Fatal("first compaction did not change the view")
	}

	// 含义相同，但线上元数据和提供方签发的 id 不同。哈希不包含这些内容，
	// 因此这仍必须是缓存命中。
	noisy := append([]llm.Message(nil), prefix...)
	noisy[1].Content = "  fix the bug  " // 仅两端用户空白不同
	noisy[2].Reasoning = "thinking"
	noisy[2].Usage = &llm.Usage{PromptTokens: 10}
	noisy[2].FinishReason = "tool_calls"
	noisy[2].ToolCalls = append([]llm.ToolCall(nil), prefix[2].ToolCalls...)
	noisy[2].ToolCalls[0].ID = "different-id"
	noisy[3].ToolCallID = "different-id"

	if _, ok := c.MaybeCompact(noisy, 100); !ok {
		t.Fatal("second compaction did not change the view")
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("metadata-only change re-ran the summariser: %d calls, want 1", got)
	}
}

func TestCachingCompactorReSummarisesWhenPrefixChanges(t *testing.T) {
	inner := &countingSummaryCompactor{}
	c := newCachingCompactor(inner, NewSummaryCache(8))

	if _, ok := c.MaybeCompact(cacheWirePrefix(), 100); !ok {
		t.Fatal("first compaction did not change the view")
	}
	changedPrefix := cacheWirePrefix()
	changedPrefix[1].Content = "fix a different bug"
	second, ok := c.MaybeCompact(changedPrefix, 100)
	if !ok {
		t.Fatal("second compaction did not change the view")
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("changed prefix re-ran the summariser %d times, want 2 total", got)
	}
	if want := "summary of fix a different bug"; second[0].Content != want {
		t.Fatalf("cached view = %q, want fresh summary %q", second[0].Content, want)
	}
}

func TestCachingCompactorConcurrentSamePrefixIsSingleFlight(t *testing.T) {
	inner := &countingSummaryCompactor{}
	c := newCachingCompactor(inner, NewSummaryCache(8))
	prefix := cacheWirePrefix()

	want, ok := c.MaybeCompact(prefix, 100)
	if !ok || len(want) == 0 {
		t.Fatal("first compaction did not change the view")
	}

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, ok := c.MaybeCompact(prefix, 100)
			if !ok || !reflect.DeepEqual(got, want) {
				errs <- "concurrent call returned a different compacted view"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("summariser called %d times under concurrent load, want 1", got)
	}
}

func TestRunnerCompactionCacheIsSharedAcrossCalls(t *testing.T) {
	r := &Runner{}
	first := r.compactionCache()
	if first == nil {
		t.Fatal("compactionCache returned nil")
	}
	if second := r.compactionCache(); second != first {
		t.Fatal("Runner did not reuse its summary cache across calls")
	}
}
