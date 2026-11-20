package session

import "testing"

func TestCompactionThresholdCappedByWhicheverComesFirst(t *testing.T) {
	cases := []struct {
		name          string
		proportional  int
		absoluteCap   int
		contextWindow int
		want          int
	}{
		// 比例阈值较小：以比例为准。
		{"proportional wins when smaller", 10_000, 32_000, 200_000, 10_000},
		// 比例阈值较大：绝对封顶生效，取先到者。
		{"absolute cap wins when smaller", 50_000, 32_000, 200_000, 32_000},
		// 两者相等：边界取相同值。
		{"equal proportional and absolute", 32_000, 32_000, 200_000, 32_000},
		// 上下文窗口比两者都小：被窗口封顶。
		{"context window caps both", 50_000, 32_000, 20_000, 20_000},
		// 只有比例时，窗口仍可封顶。
		{"context window caps proportional only", 50_000, 0, 15_000, 15_000},
		// 只有绝对上限时，窗口仍可封顶。
		{"context window caps absolute only", 0, 50_000, 12_000, 12_000},
		// 零值忽略：比例为零时取绝对上限。
		{"zero proportional ignored", 0, 32_000, 200_000, 32_000},
		// 零值忽略：绝对上限为零时取比例。
		{"zero absolute ignored", 10_000, 0, 200_000, 10_000},
		// 零窗口忽略：不额外封顶。
		{"zero context window ignored", 50_000, 32_000, 0, 32_000},
		// 负数忽略：负比例退回绝对上限。
		{"negative proportional ignored", -1, 32_000, 200_000, 32_000},
		// 负数忽略：负绝对上限退回比例。
		{"negative absolute ignored", 10_000, -5, 200_000, 10_000},
		// 负数忽略：负窗口不额外封顶。
		{"negative context window ignored", 50_000, 32_000, -1, 32_000},
		// 全部非正：无法推导阈值，返回零。
		{"all zero", 0, 0, 200_000, 0},
		{"all negative", -1, -2, -3, 0},
		{"nonpositive candidates ignore positive window", 0, -1, 200_000, 0},
		// 边界：窗口恰好等于阈值时不缩减。
		{"window equals threshold", 32_000, 32_000, 32_000, 32_000},
		{"window one below threshold", 32_000, 32_000, 31_999, 31_999},
		// 单 token 边界。
		{"single token", 1, 1, 1, 1},
		// 大数边界：不越界、不溢出。
		{"large values", 1_000_000, 2_000_000, 5_000_000, 1_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := compactionThreshold(tc.proportional, tc.absoluteCap, tc.contextWindow)
			if got != tc.want {
				t.Fatalf("compactionThreshold(%d, %d, %d) = %d, want %d",
					tc.proportional, tc.absoluteCap, tc.contextWindow, got, tc.want)
			}
		})
	}
}

// TestCompactionThresholdIsBounded 以穷举断言结果的有界性：返回值永不为负，
// 且不会超过任一参与比较的正数参数。
func TestCompactionThresholdIsBounded(t *testing.T) {
	values := []int{-3, -1, 0, 1, 7, 32, 1_000}
	for _, proportional := range values {
		for _, absoluteCap := range values {
			for _, contextWindow := range values {
				got := compactionThreshold(proportional, absoluteCap, contextWindow)
				if got < 0 {
					t.Fatalf("negative result for (%d, %d, %d): %d",
						proportional, absoluteCap, contextWindow, got)
				}
				if proportional > 0 && got > proportional {
					t.Fatalf("exceeds proportional for (%d, %d, %d): %d",
						proportional, absoluteCap, contextWindow, got)
				}
				if absoluteCap > 0 && got > absoluteCap {
					t.Fatalf("exceeds absolute cap for (%d, %d, %d): %d",
						proportional, absoluteCap, contextWindow, got)
				}
				if contextWindow > 0 && got > contextWindow {
					t.Fatalf("exceeds context window for (%d, %d, %d): %d",
						proportional, absoluteCap, contextWindow, got)
				}
			}
		}
	}
}
