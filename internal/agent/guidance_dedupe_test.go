package agent

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGuidanceStateDedupesSameSig(t *testing.T) {
	var g GuidanceState
	if !g.ShouldInject("edit:count:1") {
		t.Fatal("first injection should be allowed")
	}
	for i := 0; i < 5; i++ {
		if g.ShouldInject("edit:count:1") {
			t.Fatalf("repeated sig should be deduped on call %d", i)
		}
	}
	if got := g.Injected(); got != 1 {
		t.Fatalf("Injected() = %d, want 1", got)
	}
}

func TestGuidanceStateInjectsDifferentSig(t *testing.T) {
	var g GuidanceState
	sigs := []string{"edit:count:1", "edit:count:2", "shell:count:1", "shell:count:2"}
	for i, sig := range sigs {
		if !g.ShouldInject(sig) {
			t.Fatalf("sig %q (call %d) should be injected", sig, i)
		}
	}
	if got := g.Injected(); got != len(sigs) {
		t.Fatalf("Injected() = %d, want %d", got, len(sigs))
	}

	// 再次交替出现旧签名时，只有与最近一次不同的那个会被注入。
	if !g.ShouldInject("edit:count:1") {
		t.Fatal("older sig differing from last should be injected again")
	}
	if g.ShouldInject("edit:count:1") {
		t.Fatal("immediately repeated older sig should be deduped")
	}
}

func TestGuidanceStateResetAllowsReinject(t *testing.T) {
	var g GuidanceState
	if !g.ShouldInject("edit:count:3") {
		t.Fatal("first injection should be allowed")
	}
	if g.ShouldInject("edit:count:3") {
		t.Fatal("repeated sig should be deduped before reset")
	}

	g.Reset()
	if got := g.Injected(); got != 0 {
		t.Fatalf("Injected() after Reset = %d, want 0", got)
	}
	if !g.ShouldInject("edit:count:3") {
		t.Fatal("same sig should be injectable again after Reset")
	}
	if got := g.Injected(); got != 1 {
		t.Fatalf("Injected() after reinject = %d, want 1", got)
	}
}

func TestGuidanceStateInjectedCount(t *testing.T) {
	var g GuidanceState
	// 被去重（返回 false）的调用不得增加计数。
	for i := 0; i < 20; i++ {
		g.ShouldInject("same")
	}
	if got := g.Injected(); got != 1 {
		t.Fatalf("Injected() = %d after dedupe storm, want 1", got)
	}
	for i := 2; i <= 10; i++ {
		g.ShouldInject("same:" + strconv.Itoa(i))
	}
	if got := g.Injected(); got != 10 {
		t.Fatalf("Injected() = %d after 9 new sigs, want 10", got)
	}
}

func TestGuidanceStateConcurrentUniqueSigs(t *testing.T) {
	var g GuidanceState
	const n = 256
	var wg sync.WaitGroup
	var allowed atomic.Int64
	errCh := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if g.ShouldInject(strconv.Itoa(i)) {
				allowed.Add(1)
			} else {
				errCh <- "unique sig was deduped: " + strconv.Itoa(i)
			}
			_ = g.Injected() // 并发读，不应触发数据竞争。
		}(i)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
	if got := allowed.Load(); got != n {
		t.Fatalf("allowed = %d, want %d", got, n)
	}
	if got := g.Injected(); got != n {
		t.Fatalf("Injected() = %d, want %d", got, n)
	}
}

func TestGuidanceStateConcurrentSameSig(t *testing.T) {
	var g GuidanceState
	const n = 128
	var wg sync.WaitGroup
	var allowed atomic.Int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.ShouldInject("same") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 1 {
		t.Fatalf("allowed = %d for same sig, want 1", got)
	}
	if got := g.Injected(); got != 1 {
		t.Fatalf("Injected() = %d, want 1", got)
	}
}

func TestGuidanceStateCJKSignature(t *testing.T) {
	var g GuidanceState
	// 多字节签名按完整字符串比较，CJK 与标点不会因按字节切分而误判。
	if !g.ShouldInject("编辑:连续失败:3") {
		t.Fatal("first CJK sig should be injected")
	}
	if g.ShouldInject("编辑:连续失败:3") {
		t.Fatal("same CJK sig should be deduped")
	}
	if !g.ShouldInject("编辑:连续失败:4") {
		t.Fatal("different CJK sig should be injected")
	}
	if !g.ShouldInject("🛠️:失败:2") {
		t.Fatal("emoji-containing sig should be injected")
	}
	if got := g.Injected(); got != 3 {
		t.Fatalf("Injected() = %d, want 3", got)
	}
}

func TestGuidanceStateZeroValue(t *testing.T) {
	var g GuidanceState
	if got := g.Injected(); got != 0 {
		t.Fatalf("zero-value Injected() = %d, want 0", got)
	}
	if !g.ShouldInject("first") {
		t.Fatal("non-empty sig should inject on zero value")
	}
	if got := g.Injected(); got != 1 {
		t.Fatalf("Injected() = %d, want 1", got)
	}
	// 零值可直接 Reset，无需初始化。
	g.Reset()
	if got := g.Injected(); got != 0 {
		t.Fatalf("Injected() after reset on zero value = %d, want 0", got)
	}
}
