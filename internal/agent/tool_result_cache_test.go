package agent

import (
	"strings"
	"testing"
)

// TestToolResultCacheIdleEviction 锁定 #33：连续 5 轮未被 read 命中的
// 条目被淘汰；期间被 read 命中会重置空闲计数。
func TestToolResultCacheIdleEviction(t *testing.T) {
	c := newToolResultCache(1 << 20)
	uri := c.store(strings.Repeat("x", 1024))
	if uri == "" {
		t.Fatal("store 返回空 URI")
	}
	if _, handled := c.load(uri); !handled {
		t.Fatal("刚存入的 URI 应可读")
	}

	// 连续 4 轮空闲仍未淘汰（用 map 直接检查，避免 load 刷新 usedAt）。
	for i := 0; i < toolResultCacheIdleRounds-1; i++ {
		c.advanceIteration()
	}
	key, _ := parseToolResultCacheURI(uri)
	if _, ok := c.entries[key]; !ok {
		t.Fatal("第 4 轮空闲不应淘汰")
	}

	// 第 5 轮空闲触发淘汰；load 这时给出勿重放副作用的提示。
	c.advanceIteration()
	if _, ok := c.entries[key]; ok {
		t.Fatal("第 5 轮空闲应淘汰条目")
	}
	if got, handled := c.load(uri); !handled ||
		!strings.Contains(got, "DO NOT repeat side-effecting actions to recover output") {
		t.Fatalf("淘汰后应给出勿重放副作用提示，handled=%v got=%q", handled, got)
	}

	// 中途被 read 命中会重置空闲计数：再存一条，读一次，推进 4 轮仍在。
	uri2 := c.store(strings.Repeat("y", 32))
	if _, handled := c.load(uri2); !handled {
		t.Fatal("第二条应可读")
	}
	for i := 0; i < toolResultCacheIdleRounds-1; i++ {
		c.advanceIteration()
	}
	if got, handled := c.load(uri2); !handled || got != strings.Repeat("y", 32) {
		t.Fatalf("命中后重新计时，4 轮内不应淘汰：handled=%v got=%q", handled, got)
	}
}

// TestToolResultCacheMissNoSideEffectHint 锁定 #33：未命中时返回可读
// 提示，且提示明确要求不要重放有副作用的操作。
func TestToolResultCacheMissNoSideEffectHint(t *testing.T) {
	c := newToolResultCache(1 << 20)
	uri := toolResultCacheURI("0000000000000042")

	got, handled := c.load(uri)
	if !handled {
		t.Fatal("规范缓存 URI 应被识别")
	}
	for _, want := range []string{
		"已过期",
		"请勿重放有副作用的操作",
		"DO NOT repeat side-effecting actions to recover output",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("miss 提示缺少 %q：%q", want, got)
		}
	}

	// 非缓存 URI 必须由调用方走原有文件读取路径。
	if _, handled := c.load("some/ordinary/path.txt"); handled {
		t.Fatal("普通路径不应被当作缓存 URI")
	}
}
