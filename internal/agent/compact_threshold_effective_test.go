package agent

import (
	"math"
	"testing"
)

func TestEffectiveCompactThreshold(t *testing.T) {
	cases := []struct {
		name          string
		proportional  int
		absoluteCap   int
		contextWindow int
		want          int
	}{
		// 比例阈值是唯一正数值时，直接采用它。
		{"proportional only", 50_000, -1, -1, 50_000},
		// 绝对上限是唯一正数值时，直接采用它。
		{"absolute cap only", -1, 5_000, -1, 5_000},
		// 只有上下文窗口为正时，用它封顶。
		{"context window only", 0, 0, 100_000, 100_000},
		// 三者皆正：比例阈值最小，先到者为准。
		{"proportional wins", 1_000, 5_000, 100_000, 1_000},
		// 三者皆正：绝对上限最小。
		{"absolute cap wins", 50_000, 5_000, 100_000, 5_000},
		// 三者皆正：上下文窗口成为约束，压低结果。
		{"context window caps", 50_000, 8_000, 4_000, 4_000},
		// 绝对上限比窗口更小，先到者为准。
		{"cap beats window", 100_000, 2_000, 50_000, 2_000},
		// 三者相等时取该值。
		{"all equal", 5_000, 5_000, 5_000, 5_000},
		// 比例与绝对上限相等。
		{"proportional ties absolute cap", 5_000, 5_000, 100_000, 5_000},
		// 绝对上限与上下文窗口相等。
		{"absolute cap ties window", 50_000, 5_000, 5_000, 5_000},
		// 全零、全负与非正混合都返回 0。
		{"all zero", 0, 0, 0, 0},
		{"all negative", -1, -2, -3, 0},
		{"zero and negative mixed", -1, 0, -5, 0},
		// 非正的绝对上限与窗口被忽略，只留下比例阈值。
		{"negative caps ignored", 10_000, -1, -1, 10_000},
		{"zero window ignored", 10_000, 20_000, 0, 10_000},
		// 最小正值 1 不会被 0 或非正值吞掉。
		{"smallest positive", 1, 100, 1, 1},
		// 最大整数只参与比较，不会因封顶运算而溢出。
		{"max int bounded", math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := effectiveCompactThreshold(tc.proportional, tc.absoluteCap, tc.contextWindow); got != tc.want {
				t.Fatalf("effectiveCompactThreshold(%d, %d, %d) = %d, want %d",
					tc.proportional, tc.absoluteCap, tc.contextWindow, got, tc.want)
			}
		})
	}
}

// TestEffectiveCompactThresholdProperties 穷举小范围取值，
// 验证结果恒为所有正参数的最小值，且全非正时恒为 0。
func TestEffectiveCompactThresholdProperties(t *testing.T) {
	values := []int{-3, -1, 0, 1, 2, 7, 10}
	for _, proportional := range values {
		for _, absoluteCap := range values {
			for _, contextWindow := range values {
				want := 0
				for _, v := range []int{proportional, absoluteCap, contextWindow} {
					if v > 0 && (want == 0 || v < want) {
						want = v
					}
				}
				if got := effectiveCompactThreshold(proportional, absoluteCap, contextWindow); got != want {
					t.Fatalf("effectiveCompactThreshold(%d, %d, %d) = %d, want %d",
						proportional, absoluteCap, contextWindow, got, want)
				}
			}
		}
	}
}
