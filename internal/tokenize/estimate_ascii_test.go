package tokenize

import (
	"strings"
	"testing"
)

// estimateSink 防止编译器把只关心分配次数的 Estimate 调用优化掉。
var estimateSink int

// TestEstimateASCIIZeroAllocs 验证纯 ASCII 估算不触发任何堆分配：
// 入口探测只遍历字节并做整数运算，不构造 []byte、不计算 SHA256、不触碰缓存。
func TestEstimateASCIIZeroAllocs(t *testing.T) {
	text := strings.Repeat("abc", 10000) // 30000 字节纯 ASCII
	allocs := testing.AllocsPerRun(100, func() {
		estimateSink = Estimate(text)
	})
	if allocs != 0 {
		t.Fatalf("Estimate(纯ASCII, %d 字节) 每轮分配 = %g, want 0", len(text), allocs)
	}
}

// TestEstimateASCIIMatchesUncached 验证 ASCII 快速路径的公式与 estimateUncached
// 逐值一致，覆盖空串与所有 len%4 余数边界。
func TestEstimateASCIIMatchesUncached(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"len1", "a"},
		{"len2", "ab"},
		{"len3", "abc"},
		{"len4", "abcd"},
		{"len5", "abcde"},
		{"len6", "abcdef"},
		{"len7", "abcdefg"},
		{"len8", "abcdefgh"},
		{"large", strings.Repeat("abc", 10000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := estimateUncached(tc.text)
			if got := Estimate(tc.text); got != want {
				t.Fatalf("case %s (len=%d): Estimate = %d, estimateUncached = %d",
					tc.name, len(tc.text), got, want)
			}
		})
	}
}

// TestEstimateNonASCIIResultUnchanged 验证非 ASCII 文本的结果仍与原始计算路径
// 一致，且仍走原缓存路径：第二次调用应命中缓存且结果不变。
func TestEstimateNonASCIIResultUnchanged(t *testing.T) {
	withFreshCache(t, 8)
	cases := []struct {
		name string
		text string
	}{
		{"han", strings.Repeat("中", 100)},
		{"kana and hangul", "アイウ한글"},
		{"emoji", "\U0001F600\U0001F600"},
		{"mixed", "中文测试"},
		{"han plus ascii", "abcd中文"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := estimateUncached(tc.text)
			first := Estimate(tc.text)
			if first != want {
				t.Fatalf("Estimate(%q) = %d, estimateUncached = %d", tc.text, first, want)
			}
			beforeHits, _ := sharedCache.stats()
			if got := Estimate(tc.text); got != first {
				t.Fatalf("重复 Estimate(%q) = %d, want %d", tc.text, got, first)
			}
			afterHits, _ := sharedCache.stats()
			if afterHits != beforeHits+1 {
				t.Fatalf("重复估算命中次数 = %d, want %d", afterHits, beforeHits+1)
			}
		})
	}
}

// TestEstimateASCIIBypassesCache 验证纯 ASCII 估算完全绕过缓存：
// 既不读取也不写入 sharedCache，命中数与条目数保持不变。
func TestEstimateASCIIBypassesCache(t *testing.T) {
	withFreshCache(t, 8)
	text := strings.Repeat("abc", 10000)
	beforeHits, beforeSize := sharedCache.stats()
	got := Estimate(text)
	afterHits, afterSize := sharedCache.stats()
	if want := (len(text) + CharsPerToken - 1) / CharsPerToken; got != want {
		t.Fatalf("Estimate(纯ASCII) = %d, want %d", got, want)
	}
	if afterHits != beforeHits || afterSize != beforeSize {
		t.Fatalf("纯 ASCII 估算不应触碰缓存：hits %d -> %d, size %d -> %d",
			beforeHits, afterHits, beforeSize, afterSize)
	}
}

// TestEstimateDoesNotMutateInput 验证估算不修改入参内容。
func TestEstimateDoesNotMutateInput(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("abc", 100),    // 纯 ASCII
		strings.Repeat("abc中xyz", 64), // 含非 ASCII
	} {
		want := in
		_ = Estimate(in)
		if in != want {
			t.Fatalf("Estimate 修改了入参：got %q, want %q", in, want)
		}
	}
}
