package app

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCacheHitTextTypical 覆盖命中率可数值化的典型场景，断言整段文案的精确字节，
// 包括中文标签、全角括号与逗号，以及百分比按四舍五入到整数的结果。
func TestCacheHitTextTypical(t *testing.T) {
	for _, tc := range []struct {
		name  string
		read  int
		write int
		total int
		want  string
	}{
		{"half hit", 512, 128, 1024, "缓存命中 512 tokens（50%），写入 128"},
		{"rounded up", 2, 1, 3, "缓存命中 2 tokens（67%），写入 1"},
		{"quarter hit", 1, 0, 4, "缓存命中 1 tokens（25%），写入 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheHitText(tc.read, tc.write, tc.total); got != tc.want {
				t.Fatalf("cacheHitText(%d, %d, %d) = %q, want %q", tc.read, tc.write, tc.total, got, tc.want)
			}
		})
	}
}

// TestCacheHitTextUnavailable 保证分母（提示 token 总计）为零或负数时整段文案
// 退化为"缓存统计不可用"，无论命中/写入值是什么，绝不输出虚假百分比或负数。
func TestCacheHitTextUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		read  int
		write int
		total int
	}{
		{"all zero", 0, 0, 0},
		{"all negative", -1, -2, -3},
		{"zero total with values", 100, 50, 0},
		{"negative total with values", 100, 50, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheHitText(tc.read, tc.write, tc.total); got != cacheHitUnavailableText {
				t.Fatalf("cacheHitText(%d, %d, %d) = %q, want %q",
					tc.read, tc.write, tc.total, got, cacheHitUnavailableText)
			}
		})
	}
}

// TestCacheHitTextClampsTokens 保证负数 token 被钳制为 0、越界 token 被截断到
// total，输出不含负号或超过分母的计数。
func TestCacheHitTextClampsTokens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		read  int
		write int
		total int
		want  string
	}{
		{"both above total", 2000, 5000, 1000, "缓存命中 1000 tokens（100%），写入 1000"},
		{"both negative", -5, -9, 100, "缓存命中 0 tokens（0%），写入 0"},
		{"read negative only", -5, 500, 1000, "缓存命中 0 tokens（0%），写入 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheHitText(tc.read, tc.write, tc.total); got != tc.want {
				t.Fatalf("cacheHitText(%d, %d, %d) = %q, want %q", tc.read, tc.write, tc.total, got, tc.want)
			}
		})
	}
}

// TestCacheHitTextPercentageWithinBounds 保证百分比始终落在 0..100：
// 零命中为 0%、全命中为 100%，而极小与极大比例经四舍五入后也分别落在两端。
func TestCacheHitTextPercentageWithinBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		read  int
		write int
		total int
		want  string
	}{
		{"zero hit", 0, 0, 100, "缓存命中 0 tokens（0%），写入 0"},
		{"full hit", 100, 0, 100, "缓存命中 100 tokens（100%），写入 0"},
		{"round tiny down", 1, 0, 1000, "缓存命中 1 tokens（0%），写入 0"},
		{"round huge up", 999, 0, 1000, "缓存命中 999 tokens（100%），写入 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheHitText(tc.read, tc.write, tc.total); got != tc.want {
				t.Fatalf("cacheHitText(%d, %d, %d) = %q, want %q", tc.read, tc.write, tc.total, got, tc.want)
			}
		})
	}
}

// TestCacheHitTextExtremeInputsBounded 用极值与最大整型输入验证输出有界：
// 长度受固定上界约束，且绝不出现 NaN、Inf 或负号等伪测量值。
func TestCacheHitTextExtremeInputsBounded(t *testing.T) {
	inputs := []struct {
		read  int
		write int
		total int
	}{
		{math.MaxInt, math.MaxInt, math.MaxInt},
		{math.MinInt, math.MinInt, math.MinInt},
		{math.MaxInt, 0, math.MaxInt},
		{math.MaxInt, math.MaxInt, 1},
		{1, math.MaxInt, 1},
	}
	// 文案由固定中文标签、最多 20 位有界数值、一个 0..100 百分比与标点组成，120 字节足够宽松。
	const maxLen = 120
	for _, in := range inputs {
		got := cacheHitText(in.read, in.write, in.total)
		if len(got) > maxLen {
			t.Errorf("cacheHitText(%d, %d, %d) 长度 %d 超过上界 %d：%q",
				in.read, in.write, in.total, len(got), maxLen, got)
		}
		for _, bad := range []string{"NaN", "Inf", "+Inf", "-Inf", "-"} {
			if strings.Contains(got, bad) {
				t.Errorf("cacheHitText(%d, %d, %d) 含非法片段 %q：%q",
					in.read, in.write, in.total, bad, got)
			}
		}
	}
}

// TestCacheHitTextCJKSafe 保证可用分支输出是合法 UTF-8、包含预期的简体中文标签，
// 且没有替换字符——拼接过程从不按字节截断，因此不会出现半个汉字。
func TestCacheHitTextCJKSafe(t *testing.T) {
	got := cacheHitText(123, 45, 1000)
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("输出不是合法 UTF-8 或含替换字符：%q", got)
	}
	for _, label := range []string{cacheHitHitLabel, cacheHitWriteLabel, cacheHitTokensUnit} {
		if !strings.Contains(got, label) {
			t.Errorf("输出缺少标签 %q：%q", label, got)
		}
	}
}

// TestCacheHitTextUnavailableCJKSafe 保证不可用分支同样保持 CJK 安全与结构稳定。
func TestCacheHitTextUnavailableCJKSafe(t *testing.T) {
	got := cacheHitText(0, 0, 0)
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("不可用分支输出不是合法 UTF-8 或含替换字符：%q", got)
	}
	if got != cacheHitUnavailableText {
		t.Fatalf("不可用分支文案 = %q, want %q", got, cacheHitUnavailableText)
	}
}

// TestCacheHitTextStable 保证纯函数特性：多次调用逐字节一致，可用于缓存与复用。
func TestCacheHitTextStable(t *testing.T) {
	first := cacheHitText(512, 128, 1024)
	for i := 0; i < 3; i++ {
		if got := cacheHitText(512, 128, 1024); got != first {
			t.Fatalf("cacheHitText 不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
		}
	}
}
