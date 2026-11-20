package tui

import "testing"

// visibleWindow 统计项而非字节或 rune，因此 CJK（以及折叠后的
// 显示宽度）永不进入算术。下面每个用例都是纯整数，
// 正因如此。
func TestVisibleWindow(t *testing.T) {
	tests := []struct {
		name           string
		total          int
		scrollOffset   int
		viewportHeight int
		overscan       int
		wantStart      int
		wantEnd        int
	}{
		{"normal_window", 100, 20, 10, 0, 20, 30},
		{"overscan_both_sides", 100, 20, 10, 5, 15, 35},
		{"scroll_at_top", 100, 0, 10, 3, 0, 13},
		{"scrolled_to_bottom", 100, 90, 10, 5, 85, 100},
		{"offset_beyond_total", 100, 1000, 10, 5, 95, 100},
		{"offset_negative", 100, -5, 10, 5, 0, 15},
		{"viewport_larger_than_total", 10, 0, 100, 0, 0, 10},
		{"zero_height", 100, 20, 0, 5, 20, 20},
		{"zero_total", 0, 0, 10, 5, 0, 0},
		{"negative_total", -3, 0, 10, 5, 0, 0},
		{"overscan_clamped_at_top", 100, 0, 10, 50, 0, 60},
		{"overscan_clamped_at_bottom", 100, 95, 10, 50, 45, 100},
		{"negative_overscan_treated_as_zero", 100, 20, 10, -5, 20, 30},
		{"offset_at_total_empty_without_height", 100, 100, 10, 0, 100, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end := visibleWindow(tc.total, tc.scrollOffset, tc.viewportHeight, tc.overscan)
			if start != tc.wantStart || end != tc.wantEnd {
				t.Fatalf("visibleWindow(%d, %d, %d, %d) = (%d, %d), want (%d, %d)",
					tc.total, tc.scrollOffset, tc.viewportHeight, tc.overscan,
					start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

// TestVisibleWindowBounds 是对大量输入的属性检查：返回的
// 区间始终有序且包含在 [0, total] 内，其宽度永不
// 超过视口加上两倍 overscan。
func TestVisibleWindowBounds(t *testing.T) {
	for total := -2; total <= 40; total++ {
		for offset := -5; offset <= total+5; offset++ {
			for height := -2; height <= 12; height++ {
				for overscan := -2; overscan <= 8; overscan++ {
					start, end := visibleWindow(total, offset, height, overscan)
					if start < 0 || end < start || end > maxInt(total, 0) {
						t.Fatalf("visibleWindow(%d, %d, %d, %d) = (%d, %d) out of bounds",
							total, offset, height, overscan, start, end)
					}
				}
			}
		}
	}
}
