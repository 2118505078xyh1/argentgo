package tui

import "testing"

func TestTranscriptScrollMax(t *testing.T) {
	tests := []struct {
		name                       string
		totalLines, viewportHeight int
		want                       int
	}{
		{"normal", 100, 10, 90},
		{"exact_fit", 10, 10, 0},
		{"fits_with_room", 8, 10, 0},
		{"empty_transcript", 0, 10, 0},
		{"negative_total", -5, 10, 0},
		{"zero_viewport", 100, 0, 0},
		{"negative_viewport", 100, -3, 0},
		{"one_line_viewport", 100, 1, 99},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := transcriptScrollMax(tc.totalLines, tc.viewportHeight); got != tc.want {
				t.Fatalf("transcriptScrollMax(%d, %d) = %d, want %d",
					tc.totalLines, tc.viewportHeight, got, tc.want)
			}
		})
	}
}

func TestClampTranscriptScroll(t *testing.T) {
	tests := []struct {
		name                         string
		offset, totalLines, viewport int
		want                         int
	}{
		{"inside", 20, 100, 10, 20},
		{"at_top", 90, 100, 10, 90},
		{"past_top", 500, 100, 10, 90},
		{"past_bottom", -12, 100, 10, 0},
		{"no_scrollable_range", 5, 10, 10, 0},
		{"shrunken_transcript", 50, 20, 10, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampTranscriptScroll(tc.offset, tc.totalLines, tc.viewport); got != tc.want {
				t.Fatalf("clampTranscriptScroll(%d, %d, %d) = %d, want %d",
					tc.offset, tc.totalLines, tc.viewport, got, tc.want)
			}
		})
	}
}

func TestScrollTranscript(t *testing.T) {
	const total, viewport = 100, 10 // scrollable range [0, 90]
	tests := []struct {
		name          string
		offset, delta int
		want          int
	}{
		{"scroll_back", 20, 3, 23},
		{"scroll_forward", 20, -3, 17},
		{"reach_top_exactly", 85, 5, 90},
		{"overscroll_top_is_clamped", 85, 1000, 90},
		{"overscroll_bottom_is_clamped", 5, -100, 0},
		{"one_page_down_undoes_one_page_up", 80, -10, 70},
		{"zero_delta_reclamps", 500, 0, 90},
		{"negative_offset_reclamps", -3, 0, 0},
		{"negative_offset_with_delta", -3, 5, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrollTranscript(tc.offset, tc.delta, total, viewport); got != tc.want {
				t.Fatalf("scrollTranscript(%d, %d, %d, %d) = %d, want %d",
					tc.offset, tc.delta, total, viewport, got, tc.want)
			}
		})
	}
}

func TestScrollTranscriptSaturates(t *testing.T) {
	tests := []struct {
		name          string
		offset, delta int
		want          int
	}{
		{"max_plus_max", maxScrollInt, maxScrollInt, 90},
		{"min_plus_min", minScrollInt, minScrollInt, 0},
		{"max_plus_min", maxScrollInt, minScrollInt, 0},
		{"offset_past_max_minus_one", maxScrollInt, -1, 90},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrollTranscript(tc.offset, tc.delta, 100, 10); got != tc.want {
				t.Fatalf("scrollTranscript(%d, %d, 100, 10) = %d, want %d",
					tc.offset, tc.delta, got, tc.want)
			}
		})
	}
}

// TestScrollTranscriptBounds 是一个属性检查：任何陈旧的
// 偏移、delta、total 或 viewport 组合都不会产生越界结果，即使
// 记录比视口还短。
func TestScrollTranscriptBounds(t *testing.T) {
	for total := -2; total <= 30; total++ {
		for viewport := -2; viewport <= 12; viewport++ {
			max := transcriptScrollMax(total, viewport)
			for offset := -5; offset <= total+5; offset++ {
				for delta := -40; delta <= 40; delta++ {
					got := scrollTranscript(offset, delta, total, viewport)
					if got < 0 || got > max {
						t.Fatalf("scrollTranscript(%d, %d, %d, %d) = %d, want in [0, %d]",
							offset, delta, total, viewport, got, max)
					}
				}
			}
		}
	}
}

func TestTranscriptScrollAfterAppend(t *testing.T) {
	tests := []struct {
		name                                         string
		offset, appended, totalLines, viewportHeight int
		pinned                                       bool
		want                                         int
	}{
		{"follows_stream_when_pinned", 0, 5, 105, 10, true, 0},
		{"pinned_ignores_stale_offset", 500, 5, 105, 10, true, 0},
		{"keeps_absolute_anchor", 10, 5, 105, 10, false, 15},
		{"large_append_clamps", 80, 1000, 110, 10, false, 100},
		{"no_new_lines_reclamps", 500, 0, 20, 10, false, 10},
		{"shrink_only_reclamps", 500, -3, 20, 10, false, 10},
		{"overflow_append_clamps", 10, maxScrollInt, 105, 10, false, 95},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := transcriptScrollAfterAppend(tc.offset, tc.appended, tc.totalLines, tc.viewportHeight, tc.pinned)
			if got != tc.want {
				t.Fatalf("transcriptScrollAfterAppend(%d, %d, %d, %d, %v) = %d, want %d",
					tc.offset, tc.appended, tc.totalLines, tc.viewportHeight, tc.pinned, got, tc.want)
			}
		})
	}
}
