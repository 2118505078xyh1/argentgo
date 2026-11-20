package app

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRunStatsTextFull 覆盖三个指标都有效的典型场景，断言整段文案的精确字节，
// 包括中文标签、分隔符与吞吐按生成窗口（totalMS - firstTokenMS）计算的结果。
func TestRunStatsTextFull(t *testing.T) {
	// 生成窗口 = 2000 - 200 = 1800ms；100 / 1.8 = 55.55... -> "55.6"。
	const want = "首 token 延迟 200ms · 总耗时 2.00s · 吞吐 55.6 tokens/s"
	if got := runStatsText(200, 2000, 100); got != want {
		t.Fatalf("runStatsText(200, 2000, 100) = %q, want %q", got, want)
	}
}

// TestRunStatsTextAllUnavailable 保证负数与零输入被钳制为不可用，
// 三个指标都退化为"标签不可用"，绝不输出负数、零值或虚假数字。
func TestRunStatsTextAllUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  int64
		total  int64
		tokens int
	}{
		{"all zero", 0, 0, 0},
		{"all negative", -1, -2, -3},
		{"negative first and total", -5, -9, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const want = "首 token 延迟不可用 · 总耗时不可用 · 吞吐不可用"
			if got := runStatsText(tc.first, tc.total, tc.tokens); got != want {
				t.Fatalf("runStatsText(%d, %d, %d) = %q, want %q", tc.first, tc.total, tc.tokens, got, want)
			}
		})
	}
}

// TestRunStatsTextClampsNegativeFirstToken 保证负的首 token 延迟被钳制为不可用，
// 但总耗时与 tokens 仍有效时，吞吐改用 totalMS 作为窗口，不会因 TTFT 无效而整体失效。
func TestRunStatsTextClampsNegativeFirstToken(t *testing.T) {
	const want = "首 token 延迟不可用 · 总耗时 3.00s · 吞吐 16.7 tokens/s"
	if got := runStatsText(-5, 3000, 50); got != want {
		t.Fatalf("runStatsText(-5, 3000, 50) = %q, want %q", got, want)
	}
}

// TestRunStatsTextZeroFirstTokenFallsBackToTotal 保证零 TTFT 与负 TTFT 一样退化为
// 用整次运行耗时计算吞吐，输出保持一致。
func TestRunStatsTextZeroFirstTokenFallsBackToTotal(t *testing.T) {
	withZero := runStatsText(0, 3000, 50)
	withNegative := runStatsText(-5, 3000, 50)
	if withZero != withNegative {
		t.Fatalf("零与负 TTFT 应等价：%q vs %q", withZero, withNegative)
	}
}

// TestRunStatsTextInvertedFirstToken 保证 TTFT 大于总耗时的倒置输入不会让窗口变成
// 负数：退化为使用 totalMS，指标仍全部可数值化且不出现负号。
func TestRunStatsTextInvertedFirstToken(t *testing.T) {
	const want = "首 token 延迟 5.00s · 总耗时 2.00s · 吞吐 5.0 tokens/s"
	if got := runStatsText(5000, 2000, 10); got != want {
		t.Fatalf("runStatsText(5000, 2000, 10) = %q, want %q", got, want)
	}
}

// TestRunStatsTextDurationFormatting 覆盖毫秒/秒的格式化边界，
// 并确认恰好 1000ms 时切换为秒表示。
func TestRunStatsTextDurationFormatting(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ms    int64
		wantT string
	}{
		{"one millisecond", 1, "1ms"},
		{"999 milliseconds", 999, "999ms"},
		{"exactly one second", 1000, "1.00s"},
		{"one and half seconds", 1500, "1.50s"},
		{"whole seconds", 12000, "12.00s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runStatsDuration(tc.ms); got != tc.wantT {
				t.Fatalf("runStatsDuration(%d) = %q, want %q", tc.ms, got, tc.wantT)
			}
		})
	}
}

// TestRunStatsTextThroughputUnavailableWhenTokensMissing 保证 tokens 非正时吞吐
// 标注为不可用，而首 token 延迟与总耗时仍照常展示。
func TestRunStatsTextThroughputUnavailableWhenTokensMissing(t *testing.T) {
	for _, tokens := range []int{0, -1, -100} {
		const want = "首 token 延迟 500ms · 总耗时 2.00s · 吞吐不可用"
		if got := runStatsText(500, 2000, tokens); got != want {
			t.Fatalf("runStatsText(500, 2000, %d) = %q, want %q", tokens, got, want)
		}
	}
}

// TestRunStatsTextThroughputUnavailableWhenNoTime 保证没有任何可用时间窗口时
// 吞吐为不可用，即便 tokens 有效。
func TestRunStatsTextThroughputUnavailableWhenNoTime(t *testing.T) {
	const want = "首 token 延迟不可用 · 总耗时不可用 · 吞吐不可用"
	if got := runStatsText(0, -1, 100); got != want {
		t.Fatalf("runStatsText(0, -1, 100) = %q, want %q", got, want)
	}
}

// TestRunStatsTextExtremeInputsBounded 用极值与最大整型输入验证输出有界：
// 长度受固定上界约束，且绝不出现 NaN、Inf 或负号等伪测量值。
func TestRunStatsTextExtremeInputsBounded(t *testing.T) {
	inputs := []struct {
		first  int64
		total  int64
		tokens int
	}{
		{math.MaxInt64, math.MaxInt64, math.MaxInt},
		{math.MinInt64, math.MinInt64, math.MinInt},
		{1, math.MaxInt64, 1},
		{math.MaxInt64 - 1, math.MaxInt64, math.MaxInt},
		{1, 1, math.MaxInt},
	}
	// 每段文案由三段固定标签、最多 21 位有界数值与两个分隔符组成，200 字节足够宽松。
	const maxLen = 200
	for _, in := range inputs {
		got := runStatsText(in.first, in.total, in.tokens)
		if len(got) > maxLen {
			t.Errorf("runStatsText(%d, %d, %d) 长度 %d 超过上界 %d：%q",
				in.first, in.total, in.tokens, len(got), maxLen, got)
		}
		for _, bad := range []string{"NaN", "Inf", "+Inf", "-Inf", "-"} {
			if strings.Contains(got, bad) {
				t.Errorf("runStatsText(%d, %d, %d) 含非法片段 %q：%q",
					in.first, in.total, in.tokens, bad, got)
			}
		}
	}
}

// TestRunStatsTextCJKSafe 保证输出是合法 UTF-8、包含预期的简体中文标签，
// 且没有替换字符——拼接过程从不按字节截断，因此不会出现半个汉字。
func TestRunStatsTextCJKSafe(t *testing.T) {
	got := runStatsText(123, 4567, 42)
	if !utf8.ValidString(got) {
		t.Fatalf("输出不是合法 UTF-8：%q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("输出包含替换字符：%q", got)
	}
	for _, label := range []string{runStatsTTFTLabel, runStatsTotalLabel, runStatsThroughputLabel} {
		if !strings.Contains(got, label) {
			t.Errorf("输出缺少中文标签 %q：%q", label, got)
		}
	}
}

// TestRunStatsTextAllUnavailableCJKSafe 保证全不可用分支同样保持 CJK 安全与结构稳定。
func TestRunStatsTextAllUnavailableCJKSafe(t *testing.T) {
	got := runStatsText(0, 0, 0)
	if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("不可用分支输出不是合法 UTF-8：%q", got)
	}
	if strings.Count(got, runStatsSeparator) != 2 {
		t.Fatalf("不可用分支分隔符数量应为 2：%q", got)
	}
}

// TestRunStatsTextStable 保证纯函数特性：多次调用逐字节一致，可用于缓存与复用。
func TestRunStatsTextStable(t *testing.T) {
	first := runStatsText(200, 2000, 100)
	for i := 0; i < 3; i++ {
		if got := runStatsText(200, 2000, 100); got != first {
			t.Fatalf("runStatsText 不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
		}
	}
}
