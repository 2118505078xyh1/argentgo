package app

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
)

// delayedCompactor 用可控延迟模拟"摘要计算很慢"，并记录同步路径调用次数。
type delayedCompactor struct {
	delay    time.Duration
	calls    atomic.Int32
	prepared []llm.Message
	calledAt atomic.Int64
	fail     bool
}

func (d *delayedCompactor) MaybeCompact(messages []llm.Message, window int) ([]llm.Message, bool) {
	d.calls.Add(1)
	d.calledAt.Store(time.Now().UnixNano())
	if d.fail {
		return messages, false
	}
	time.Sleep(d.delay)
	out := []llm.Message{{Role: "system", Content: "prepared summary"}}
	out = append(out, messages[len(messages)-1])
	d.prepared = out
	return out, true
}

func manyMessages(n int) []llm.Message {
	out := make([]llm.Message, 0, n)
	out = append(out, llm.Message{Role: "system", Content: "system"})
	for i := 1; i < n; i++ {
		out = append(out, llm.Message{Role: "user", Content: strings.Repeat("x", 40)})
	}
	return out
}

func TestBackgroundCompactEnabledFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", true}, {"1", true}, {"true", true}, {" YES ", true}, {"maybe", true},
		{"0", false}, {"false", false}, {" NO ", false}, {"off", false},
	}
	for _, tc := range cases {
		if got := backgroundCompactEnabled(func(string) string { return tc.raw }); got != tc.want {
			t.Errorf("enabled(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
	if !backgroundCompactEnabled(nil) {
		t.Fatal("nil env must default to on")
	}
}

func TestBackgroundCompactDisabledReturnsInner(t *testing.T) {
	inner := &delayedCompactor{}
	got, observer := newBackgroundCompactor(inner, func(string) string { return "0" })
	if got != inner || observer != nil {
		t.Fatal("disabled path must return inner unchanged with nil observer")
	}
	if observer != nil {
		observer(manyMessages(50), 100)
	}
	if inner.calls.Load() != 0 {
		t.Fatal("disabled observer must not trigger compaction")
	}
}

func TestBackgroundCompactZeroBlockAtThreshold(t *testing.T) {
	inner := &delayedCompactor{delay: 150 * time.Millisecond}
	compactor, observer := newBackgroundCompactor(inner, func(string) string { return "1" })
	messages := manyMessages(60)
	threshold := 100

	// 压力达到 80% 时触发后台预计算，等它完成。
	observer(messages, threshold)
	deadline := time.Now().Add(2 * time.Second)
	for {
		real := compactor.(*backgroundCompactor)
		real.mu.Lock()
		done := real.cached != nil
		real.mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	before := inner.calls.Load()
	start := time.Now()
	got, changed := compactor.MaybeCompact(messages, agent.CompactWindow(threshold))
	elapsed := time.Since(start)
	if !changed || len(got) == 0 {
		t.Fatal("cached compact result not delivered")
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("threshold switch blocked %v, want zero-block cached delivery", elapsed)
	}
	if inner.calls.Load() != before {
		t.Fatal("cached delivery must not call inner synchronously")
	}
}

func TestBackgroundCompactRejectsLongTail(t *testing.T) {
	inner := &delayedCompactor{delay: 10 * time.Millisecond}
	compactor, observer := newBackgroundCompactor(inner, func(string) string { return "1" })
	messages := manyMessages(60)
	observer(messages, 100)

	real := compactor.(*backgroundCompactor)
	deadline := time.Now().Add(2 * time.Second)
	for {
		real.mu.Lock()
		done := real.cached != nil
		real.mu.Unlock()
		if done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 在锚点之后追加大量消息 → tail 超过上限，必须拒绝缓存并同步回退。
	grown := append(messages, manyMessages(backgroundCompactMaxTail+5)...)
	before := inner.calls.Load()
	_, changed := compactor.MaybeCompact(grown, agent.CompactWindow(100))
	if !changed {
		t.Fatal("sync fallback should still compact")
	}
	if inner.calls.Load() != before+1 {
		t.Fatalf("long tail must fall back to sync inner, calls %d -> %d", before, inner.calls.Load())
	}
}

func TestBackgroundCompactFailureFallsBackToSync(t *testing.T) {
	inner := &delayedCompactor{fail: true}
	compactor, observer := newBackgroundCompactor(inner, func(string) string { return "1" })
	messages := manyMessages(60)
	observer(messages, 100)
	time.Sleep(30 * time.Millisecond) // 让后台失败完成

	before := inner.calls.Load()
	_, changed := compactor.MaybeCompact(messages, agent.CompactWindow(100))
	// 失败时缓存为空 → 同步回退调用 inner（仍失败，返回 unchanged）。
	if changed {
		t.Fatal("failed background compaction must not deliver a cached result")
	}
	if inner.calls.Load() != before+1 {
		t.Fatalf("failure must fall back to sync inner, calls %d -> %d", before, inner.calls.Load())
	}
}
