package agent

import (
	"errors"
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// streamFingerprint 模拟流式采集：按顺序合并增量后用最终累积值算指纹。
func streamFingerprint(deltas []llm.ToolCallDelta) string {
	var acc llm.ToolCallDelta
	for _, d := range deltas {
		acc = llm.MergeToolDelta(acc, d)
	}
	return SpeculativeFingerprint(acc.Name, acc.Arguments)
}

func TestFinalFingerprints(t *testing.T) {
	tests := []struct {
		name  string
		reply llm.Message
		want  []string
	}{
		{
			name:  "empty reply",
			reply: llm.Message{Role: "assistant"},
			want:  []string{},
		},
		{
			name: "single call empty args",
			reply: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
				{Function: llm.ToolCallFunction{Name: "read"}},
			}},
			want: []string{fpExpected(t, "read", "{}")},
		},
		{
			name: "multiple calls keep order",
			reply: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
				{Function: llm.ToolCallFunction{Name: "grep", Arguments: `{"pattern":"x"}`}},
				{Function: llm.ToolCallFunction{Name: "ls", Arguments: `{"path":"."}`}},
			}},
			want: []string{
				fpExpected(t, "grep", `{"pattern":"x"}`),
				fpExpected(t, "ls", `{"path":"."}`),
			},
		},
		{
			name: "cjk name and args",
			reply: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
				{Function: llm.ToolCallFunction{Name: "读取", Arguments: `{"路径":"中文"}`}},
			}},
			want: []string{fpExpected(t, "读取", `{"路径":"中文"}`)},
		},
		{
			name: "arguments normalization key order",
			reply: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
				{Function: llm.ToolCallFunction{Name: "grep", Arguments: `{"b":2,"a":1}`}},
			}},
			want: []string{fpExpected(t, "grep", `{"a":1,"b":2}`)},
		},
		{
			name: "duplicate calls both retained",
			reply: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
				{Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"."}`}},
				{Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"."}`}},
			}},
			want: []string{
				SpeculativeFingerprint("read", `{"path":"."}`),
				SpeculativeFingerprint("read", `{"path":"."}`),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FinalFingerprints(tt.reply)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("FinalFingerprints() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFinalFingerprintsDoesNotModifyReply(t *testing.T) {
	reply := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "1", Function: llm.ToolCallFunction{Name: "grep", Arguments: `{"pattern":"中文"}`}},
		{ID: "2", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"."}`}},
	}}
	// 深拷贝一份快照，逐字段独立复制。
	snapshot := llm.Message{Role: reply.Role, ToolCalls: make([]llm.ToolCall, len(reply.ToolCalls))}
	for i, call := range reply.ToolCalls {
		snapshot.ToolCalls[i] = llm.ToolCall{
			ID: call.ID, Type: call.Type,
			Function: llm.ToolCallFunction{Name: call.Function.Name, Arguments: call.Function.Arguments},
		}
	}

	fps := FinalFingerprints(reply)
	if len(fps) != len(reply.ToolCalls) {
		t.Fatalf("len(fps) = %d, want %d", len(fps), len(reply.ToolCalls))
	}
	if !reflect.DeepEqual(reply, snapshot) {
		t.Fatalf("reply mutated: got %+v, want %+v", reply, snapshot)
	}
}

func TestFinalFingerprintsMatchesStreaming(t *testing.T) {
	// 流式增量按到达顺序累积，最终 reply 的权威 ToolCalls 与累积结果一致。
	final := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "1", Function: llm.ToolCallFunction{Name: "grep", Arguments: `{"pattern":"x"}`}},
		{ID: "2", Function: llm.ToolCallFunction{Name: "读取", Arguments: `{"路径":"中文"}`}},
	}}
	streams := [][]llm.ToolCallDelta{
		{
			{Index: 0, ID: "1", Name: "grep"},
			{Index: 0, Arguments: `{"pattern":`},
			{Index: 0, Arguments: `"x"}`},
		},
		{
			{Index: 1, ID: "2", Name: "读取"},
			{Index: 1, Arguments: `{"路径":`},
			{Index: 1, Arguments: `"中文"}`},
		},
	}

	finalFPs := FinalFingerprints(final)
	if len(finalFPs) != len(streams) {
		t.Fatalf("len(finalFPs) = %d, want %d", len(finalFPs), len(streams))
	}
	for i, deltas := range streams {
		want := streamFingerprint(deltas)
		if finalFPs[i] != want {
			t.Fatalf("call %d: final fp %s != streaming fp %s", i, finalFPs[i], want)
		}
	}

	// responses done 权威覆盖：流式字节与最终调用字节不同，必须以最终值为准。
	streamed := []llm.ToolCallDelta{
		{Index: 0, ID: "1", Name: "read"},
		{Index: 0, Arguments: `{"path":"./partial"}`},
	}
	authoritative := llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "1", Function: llm.ToolCallFunction{Name: "read", Arguments: `{"path":"./final"}`}},
	}}
	finalFP := FinalFingerprints(authoritative)[0]
	streamFP := streamFingerprint(streamed)
	if finalFP == streamFP {
		t.Fatal("final fingerprint unexpectedly equals streamed fingerprint")
	}
	if want := SpeculativeFingerprint("read", `{"path":"./final"}`); finalFP != want {
		t.Fatalf("final fp = %s, want authoritative fp %s", finalFP, want)
	}
}

func TestSpecCachePruneTo(t *testing.T) {
	fpA := SpeculativeFingerprint("a", `{}`)
	fpB := SpeculativeFingerprint("b", `{}`)
	fpC := SpeculativeFingerprint("读取", `{"路径":"中文"}`)
	partialErr := errors.New("partial")

	t.Run("keep subset", func(t *testing.T) {
		var c SpecCache
		c.Set(fpA, SpecResult{Output: "a"})
		c.Set(fpB, SpecResult{Output: "b"})
		c.Set(fpC, SpecResult{Output: "c", Err: partialErr})

		valid := []string{fpA, fpC}
		snapshot := append([]string(nil), valid...)
		SpecCachePruneTo(&c, valid)

		if !reflect.DeepEqual(valid, snapshot) {
			t.Fatalf("valid mutated: got %v, want %v", valid, snapshot)
		}
		if got, ok := c.Get(fpA); !ok || got.Output != "a" {
			t.Fatalf("kept fpA = %+v ok=%v", got, ok)
		}
		if got, ok := c.Get(fpC); !ok || got.Output != "c" || !errors.Is(got.Err, partialErr) {
			t.Fatalf("kept fpC = %+v ok=%v", got, ok)
		}
		if _, ok := c.Get(fpB); ok {
			t.Fatal("pruned fpB still hits")
		}
	})

	t.Run("nil valid clears", func(t *testing.T) {
		var c SpecCache
		c.Set(fpA, SpecResult{Output: "a"})
		c.Set(fpB, SpecResult{Output: "b"})
		SpecCachePruneTo(&c, nil)
		for _, fp := range []string{fpA, fpB} {
			if _, ok := c.Get(fp); ok {
				t.Fatalf("fp %s still hits after nil prune", fp)
			}
		}
	})

	t.Run("empty valid clears", func(t *testing.T) {
		var c SpecCache
		c.Set(fpA, SpecResult{Output: "a"})
		SpecCachePruneTo(&c, []string{})
		if _, ok := c.Get(fpA); ok {
			t.Fatal("fpA still hits after empty prune")
		}
	})

	t.Run("nil receiver is no-op", func(t *testing.T) {
		var c *SpecCache
		SpecCachePruneTo(c, []string{fpA})
		SpecCachePruneTo(c, nil)
	})

	t.Run("does not alter round or stale entries", func(t *testing.T) {
		var c SpecCache
		c.Set(fpA, SpecResult{Output: "stale"})
		c.InvalidateRound()
		c.Set(fpB, SpecResult{Output: "fresh"})
		// 只保留属于旧轮的 fpA，丢弃当前轮的 fpB。
		SpecCachePruneTo(&c, []string{fpA})
		if _, ok := c.Get(fpA); ok {
			t.Fatal("stale fpA became valid after prune (round altered)")
		}
		if _, ok := c.Get(fpB); ok {
			t.Fatal("pruned fpB still hits")
		}
		// 新一轮写入仍正常工作，说明 round 未被改动。
		c.Set(fpB, SpecResult{Output: "fresh2"})
		if got, ok := c.Get(fpB); !ok || got.Output != "fresh2" {
			t.Fatalf("write after prune failed: got %+v ok=%v", got, ok)
		}
	})
}

func TestCacheHitsFor(t *testing.T) {
	fpA := SpeculativeFingerprint("a", `{}`)
	fpB := SpeculativeFingerprint("b", `{}`)
	fpC := SpeculativeFingerprint("读取", `{"路径":"中文"}`)
	missing := SpeculativeFingerprint("missing", `{}`)
	boomErr := errors.New("boom")

	newCache := func() *SpecCache {
		c := &SpecCache{}
		c.Set(fpA, SpecResult{Output: "a"})
		c.Set(fpB, SpecResult{Output: "b", Err: boomErr})
		c.Set(fpC, SpecResult{Output: "中文"})
		return c
	}

	t.Run("ordered hits and misses", func(t *testing.T) {
		c := newCache()
		final := []string{fpB, missing, fpA, fpC, fpB}
		snapshot := append([]string(nil), final...)
		results, hits := CacheHitsFor(c, final)

		wantResults := []SpecResult{
			{Output: "b", Err: boomErr},
			{},
			{Output: "a"},
			{Output: "中文"},
			{Output: "b", Err: boomErr},
		}
		wantHits := []bool{true, false, true, true, true}

		if !reflect.DeepEqual(final, snapshot) {
			t.Fatalf("final mutated: got %v, want %v", final, snapshot)
		}
		if len(results) != len(wantResults) || len(hits) != len(wantHits) {
			t.Fatalf("lengths = %d/%d, want %d/%d", len(results), len(hits), len(wantResults), len(wantHits))
		}
		for i := range wantHits {
			if hits[i] != wantHits[i] {
				t.Errorf("hits[%d] = %v, want %v", i, hits[i], wantHits[i])
			}
			if results[i].Output != wantResults[i].Output || !errors.Is(results[i].Err, wantResults[i].Err) {
				t.Errorf("results[%d] = %+v, want %+v", i, results[i], wantResults[i])
			}
		}
		// 纯读：调用后原有条目仍可正常命中。
		for _, fp := range []string{fpA, fpB, fpC} {
			if _, ok := c.Get(fp); !ok {
				t.Fatalf("cache damaged by CacheHitsFor: fp %s missing", fp)
			}
		}
	})

	t.Run("cjk fingerprint", func(t *testing.T) {
		c := newCache()
		results, hits := CacheHitsFor(c, []string{fpC})
		if len(hits) != 1 || !hits[0] || results[0].Output != "中文" {
			t.Fatalf("cjk lookup = %+v / %v", results, hits)
		}
	})

	t.Run("empty final", func(t *testing.T) {
		c := newCache()
		results, hits := CacheHitsFor(c, nil)
		if len(results) != 0 || len(hits) != 0 {
			t.Fatalf("empty final => %d/%d, want 0/0", len(results), len(hits))
		}
		results, hits = CacheHitsFor(c, []string{})
		if len(results) != 0 || len(hits) != 0 {
			t.Fatalf("empty slice => %d/%d, want 0/0", len(results), len(hits))
		}
	})

	t.Run("nil cache all miss", func(t *testing.T) {
		var c *SpecCache
		results, hits := CacheHitsFor(c, []string{fpA, fpB})
		if len(results) != 2 || len(hits) != 2 {
			t.Fatalf("lengths = %d/%d, want 2/2", len(results), len(hits))
		}
		for i := range hits {
			if hits[i] || results[i].Output != "" || results[i].Err != nil {
				t.Fatalf("nil cache index %d = %+v / %v, want zero+false", i, results[i], hits[i])
			}
		}
	})

	t.Run("stale entry counts as miss", func(t *testing.T) {
		var c SpecCache
		c.Set(fpA, SpecResult{Output: "old"})
		c.InvalidateRound()
		results, hits := CacheHitsFor(&c, []string{fpA})
		if len(hits) != 1 || hits[0] || results[0].Output != "" {
			t.Fatalf("stale entry = %+v / %v, want zero+false", results, hits)
		}
	})
}
