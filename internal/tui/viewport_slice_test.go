package tui

import (
	"math"
	"reflect"
	"testing"
	"unicode/utf8"
)

// TestViewportSliceBasic 覆盖正常切片、不足到末尾、精确边界与空结果。
func TestViewportSliceBasic(t *testing.T) {
	entries := []string{"l0", "l1", "l2", "l3", "l4", "l5"}
	tests := []struct {
		name   string
		start  int
		height int
		want   []string
	}{
		{name: "middle_window", start: 2, height: 3, want: []string{"l2", "l3", "l4"}},
		{name: "from_top", start: 0, height: 2, want: []string{"l0", "l1"}},
		{name: "height_exceeds_remaining", start: 4, height: 10, want: []string{"l4", "l5"}},
		{name: "exact_end", start: 3, height: 3, want: []string{"l3", "l4", "l5"}},
		{name: "one_row", start: 5, height: 1, want: []string{"l5"}},
		{name: "start_at_end", start: len(entries), height: 3, want: nil},
		{name: "start_beyond_end", start: 100, height: 3, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := viewportSlice(entries, tc.start, tc.height)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("viewportSlice(start=%d, height=%d) = %q, want %q",
					tc.start, tc.height, got, tc.want)
			}
		})
	}
}

// TestViewportSliceNegativeStartClampsToZero 负 start 应钳到 0，而不是 panic。
func TestViewportSliceNegativeStartClampsToZero(t *testing.T) {
	entries := []string{"a", "b", "c"}
	want := []string{"a", "b"}
	for _, start := range []int{-1, -5, math.MinInt32} {
		got := viewportSlice(entries, start, 2)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("viewportSlice(start=%d, height=2) = %q, want %q", start, got, want)
		}
	}
}

// TestViewportSliceNonPositiveHeight 非正高度不产生任何行，无论 start。
func TestViewportSliceNonPositiveHeight(t *testing.T) {
	entries := []string{"a", "b", "c"}
	for _, height := range []int{0, -1, math.MinInt32} {
		for _, start := range []int{-2, 0, 1, 3, 100} {
			if got := viewportSlice(entries, start, height); len(got) != 0 {
				t.Fatalf("viewportSlice(start=%d, height=%d) = %q, want empty", start, height, got)
			}
		}
	}
}

// TestViewportSliceEmptyInput nil 与空切片都返回空结果。
func TestViewportSliceEmptyInput(t *testing.T) {
	for _, entries := range [][]string{nil, {}} {
		if got := viewportSlice(entries, 0, 5); len(got) != 0 {
			t.Fatalf("空输入应返回空, 得到 %q", got)
		}
	}
}

// TestViewportSliceCJKAndGraphemeSafe 按元素切片使每个多字节行保持完整：
// 汉字、日文、emoji 与 ZWJ 序列都不会在字节中途被切开，输出仍是合法 UTF-8。
func TestViewportSliceCJKAndGraphemeSafe(t *testing.T) {
	entries := []string{
		"第一行的思考内容",
		"第二行包含 emoji 🚀 与汉字",
		"第三行👨‍👩‍👧‍👦 是 ZWJ 序列",
		"第四行以中文结尾。",
		"日本語の行も安全です",
	}
	got := viewportSlice(entries, 1, 3)
	want := []string{entries[1], entries[2], entries[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CJK 视口切片 = %q, want %q", got, want)
	}
	for _, text := range got {
		if !utf8.ValidString(text) {
			t.Fatalf("输出含损坏的 UTF-8: %q", text)
		}
	}
}

// TestViewportSliceStableAndNoAliasing 结果顺序稳定、可重复，且写结果不会
// 污染输入：返回独立副本，与同包其他 CJK 安全切片函数的约定一致。
func TestViewportSliceStableAndNoAliasing(t *testing.T) {
	entries := []string{"甲", "乙", "丙", "丁"}
	first := viewportSlice(entries, 1, 3)
	second := viewportSlice(entries, 1, 3)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("相同输入结果不稳定: %q vs %q", first, second)
	}
	want := []string{"乙", "丙", "丁"}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("顺序未保留: %q, want %q", first, want)
	}
	first[0] = "已改动"
	if entries[1] != "乙" || entries[2] != "丙" || entries[3] != "丁" {
		t.Fatalf("改动返回值污染了输入: %q", entries)
	}
}

// TestViewportSliceHugeHeightNoOverflow 极大 height（含 MaxInt）不会因
// start+height 溢出而越界或 panic，应钳到末尾。
func TestViewportSliceHugeHeightNoOverflow(t *testing.T) {
	entries := []string{"a", "b", "c"}
	want := []string{"b", "c"}
	for _, height := range []int{math.MaxInt, math.MaxInt32, 1 << 40} {
		got := viewportSlice(entries, 1, height)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("height=%d 溢出后 = %q, want %q", height, got, want)
		}
	}
}

// TestViewportSliceProperties 属性检查：对各式 start/height，输出始终是
// 输入的一段连续子序列，元素逐字相等，长度与边界规则完全一致。
func TestViewportSliceProperties(t *testing.T) {
	entries := make([]string, 12)
	for i := range entries {
		entries[i] = "行-" + string(rune('A'+i)) + "-汉字"
	}
	total := len(entries)
	for start := -3; start <= total+3; start++ {
		for height := -2; height <= total+3; height++ {
			got := viewportSlice(entries, start, height)

			begin := start
			if begin < 0 {
				begin = 0
			}
			var wantLen int
			switch {
			case height <= 0 || begin >= total:
				wantLen = 0
			default:
				wantLen = minInt(height, total-begin)
			}
			if len(got) != wantLen {
				t.Fatalf("viewportSlice(%d,%d) 长度 = %d, want %d", start, height, len(got), wantLen)
			}
			for i, text := range got {
				if text != entries[begin+i] {
					t.Fatalf("viewportSlice(%d,%d)[%d] = %q, want %q",
						start, height, i, text, entries[begin+i])
				}
			}
		}
	}
}
