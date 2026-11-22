package tokenize

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// withFreshCache 用给定容量的新缓存替换共享缓存，并在测试结束后恢复。
// 本包测试不调用 t.Parallel，因此临时替换全局缓存是安全的。
func withFreshCache(t *testing.T, capacity int) {
	t.Helper()
	old := sharedCache
	sharedCache = newEstimateCache(capacity)
	t.Cleanup(func() { sharedCache = old })
}

func TestEstimateCacheHitAndMiss(t *testing.T) {
	// 这里只覆盖走缓存的非 ASCII 文本；纯 ASCII 已改为绕过缓存，
	// 由 TestEstimateASCIIBypassesCache 单独验证。
	cases := []struct {
		name string
		text string
	}{
		{"wide", "中文测试"},
		{"mixed", "abc中文def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFreshCache(t, 8)

			beforeHits, _ := sharedCache.stats()
			first := Estimate(tc.text)
			afterFirstHits, sizeAfterFirst := sharedCache.stats()
			if afterFirstHits != beforeHits {
				t.Fatalf("首次估算意外命中缓存：hits %d -> %d", beforeHits, afterFirstHits)
			}
			if sizeAfterFirst != 1 {
				t.Fatalf("首次估算后缓存条目数 = %d, want 1", sizeAfterFirst)
			}

			// 重复估算同一内容应命中缓存，且结果逐字节不变。
			for i := 0; i < 3; i++ {
				if got := Estimate(tc.text); got != first {
					t.Fatalf("第 %d 次重复估算 = %d, want %d", i+1, got, first)
				}
			}
			afterHits, sizeAfter := sharedCache.stats()
			if want := afterFirstHits + 3; afterHits != want {
				t.Fatalf("重复估算命中次数 = %d, want %d", afterHits, want)
			}
			if sizeAfter != 1 {
				t.Fatalf("重复估算后缓存条目数 = %d, want 1", sizeAfter)
			}
		})
	}
}

func TestEstimateCacheDifferentContentDoesNotCollide(t *testing.T) {
	withFreshCache(t, 8)
	// 全部使用非 ASCII 文本，确保它们确实进入缓存后再验证不误命中。
	texts := []string{
		"中文测试",
		"abc中文def",
		"アイウ한글",
		strings.Repeat("中", 100),
	}
	// 预填充缓存。
	for _, text := range texts {
		Estimate(text)
	}

	// 每个内容都应取回自己的结果：哈希与完整文本必须同时匹配。
	for _, text := range texts {
		got, ok := sharedCache.get(hashContent(text), text)
		if !ok {
			t.Fatalf("内容 %q 未能从缓存命中", text)
		}
		if want := estimateUncached(text); got != want {
			t.Fatalf("内容 %q 的缓存值 = %d, want %d", text, got, want)
		}
	}

	// 用 A 的哈希搭配 B 的文本查询必须未命中，验证不会误命中。
	if _, ok := sharedCache.get(hashContent(texts[0]), texts[1]); ok {
		t.Fatal("不同文本复用同一哈希键时发生了误命中")
	}
}

func TestEstimateCacheConcurrentUse(t *testing.T) {
	withFreshCache(t, 16)
	texts := []string{
		"hello world",
		"中文测试",
		"abc中文def",
		strings.Repeat("a", 50),
		"\U0001F600\U0001F600",
	}
	// 先用未缓存路径算出期望值，作为并发断言基准。
	want := make([]int, len(texts))
	for i, text := range texts {
		want[i] = estimateUncached(text)
	}

	const goroutines = 32
	const iterations = 200
	var wg sync.WaitGroup
	errCh := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				idx := (g*iterations + i) % len(texts)
				if got := Estimate(texts[idx]); got != want[idx] {
					errCh <- fmt.Sprintf("goroutine %d: Estimate(%q) = %d, want %d", g, texts[idx], got, want[idx])
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}

	_, size := sharedCache.stats()
	if size > 16 {
		t.Fatalf("并发写入后缓存条目数 = %d, 超过上限 16", size)
	}
}

func TestEstimateCacheCapacityBound(t *testing.T) {
	cases := []struct {
		name     string
		capacity int
		inserted int
		wantSize int
		keptFrom int // 下标 >= keptFrom 的条目应仍在缓存，< keptFrom 的应已被淘汰
	}{
		{"small capacity evicts oldest", 3, 10, 3, 7},
		{"exact capacity keeps all", 4, 4, 4, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := newEstimateCache(tc.capacity)
			for i := 0; i < tc.inserted; i++ {
				text := fmt.Sprintf("content-%d", i)
				cache.put(hashContent(text), text, i)
			}
			_, size := cache.stats()
			if size != tc.wantSize {
				t.Fatalf("缓存条目数 = %d, want %d", size, tc.wantSize)
			}
			for i := 0; i < tc.keptFrom; i++ {
				text := fmt.Sprintf("content-%d", i)
				if _, ok := cache.get(hashContent(text), text); ok {
					t.Fatalf("应被淘汰的条目 %q 仍在缓存中", text)
				}
			}
			for i := tc.keptFrom; i < tc.inserted; i++ {
				text := fmt.Sprintf("content-%d", i)
				if _, ok := cache.get(hashContent(text), text); !ok {
					t.Fatalf("最近的条目 %q 被错误淘汰", text)
				}
			}
		})
	}
}

// TestEstimateMatchesUncachedAcrossCorpus 验证缓存层的存在不改变任何输入的输出：
// 用小容量缓存强制命中与淘汰交替发生，结果必须与原始计算路径逐字节一致。
func TestEstimateMatchesUncachedAcrossCorpus(t *testing.T) {
	withFreshCache(t, 4)
	corpus := []string{
		"", "a", "abcd", "abcde", strings.Repeat("a", 400),
		strings.Repeat("中", 100), "アイウ한글", "\U0001F600\U0001F600",
		"中文测试", "abcd中文",
	}
	for round := 0; round < 2; round++ {
		for _, text := range corpus {
			if got, want := Estimate(text), estimateUncached(text); got != want {
				t.Fatalf("round %d: Estimate(%q) = %d, want %d", round, text, got, want)
			}
		}
	}
}
