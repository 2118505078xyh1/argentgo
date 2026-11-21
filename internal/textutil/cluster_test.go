package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// containsInt 报告 xs 是否包含 v。
func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// TestClusterBoundaryBeforeAfter 直接校验两个边界函数的方向性：
// before 只回退（<= at），after 只前进（>= at），且结果都是簇边界。
// 语料迁自 tools 包原字素专项测试，并补充 ZWNJ/变体选择符/emoji 标签/
// 越界输入等边界条件。
func TestClusterBoundaryBeforeAfter(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		at         int
		wantBefore int
		wantAfter  int
	}{
		{"mid combining mark", "e\u0301a", 2, 0, 3},
		{"before combining mark", "e\u0301a", 1, 0, 3},
		{"after combining mark", "e\u0301a", 3, 3, 3},
		{"mid ZWJ", "👨‍👩", 7, 0, 11}, // 👨(0-3) ZWJ(4-6) 👩(7-10)
		{"mid RI flag", "🇨🇳", 4, 0, 8},
		{"mid skin tone", "👍🏽", 4, 0, 8},
		{"mid jamo", "각", 3, 0, 9},
		{"mid CRLF", "a\r\nb", 2, 1, 3},
		{"invalid byte stays its own rune", "a\xFFb", 2, 2, 2},
		{"variation selector", "❤️x", 3, 0, 6}, // ❤(0-2) VS16(3-5) x(6)
		{"ZWNJ attaches to base", "e\u200Cb", 2, 0, 4},
		{"ZWNJ cluster ends at base", "e\u200Cb", 4, 4, 4},
		{"start is a boundary", "e\u0301a", 0, 0, 0},
		{"negative at", "e\u0301a", -1, 0, 0},
		{"at end", "e\u0301a", 4, 4, 4},
		{"past end clamps", "e\u0301a", 99, 4, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClusterBoundaryBefore(tc.text, tc.at); got != tc.wantBefore {
				t.Errorf("ClusterBoundaryBefore(%q, %d) = %d, want %d", tc.text, tc.at, got, tc.wantBefore)
			}
			if got := ClusterBoundaryAfter(tc.text, tc.at); got != tc.wantAfter {
				t.Errorf("ClusterBoundaryAfter(%q, %d) = %d, want %d", tc.text, tc.at, got, tc.wantAfter)
			}
		})
	}
}

// TestGluesRules 覆盖粘合规则的每一条分支，以及 invalidRuneID 哨兵
// 永不粘合、粘合的方向性（extend/ZWJ 只向一侧粘）等语义。
func TestGluesRules(t *testing.T) {
	invalid := ClusterRune(utf8.RuneError, 1) // 必须是 InvalidRuneID
	if invalid != InvalidRuneID {
		t.Fatalf("ClusterRune(RuneError, 1) = %d, want InvalidRuneID %d", invalid, InvalidRuneID)
	}
	// 合法的 U+FFFD（size==3）按普通 rune 处理，不是哨兵。
	if got := ClusterRune(utf8.RuneError, 3); got != utf8.RuneError {
		t.Fatalf("ClusterRune(RuneError, 3) = %d, want %d", got, utf8.RuneError)
	}

	cases := []struct {
		name      string
		prev, cur rune
		want      bool
	}{
		{"CRLF", '\r', '\n', true},
		{"LF then CR is not a cluster", '\n', '\r', false},
		{"ZWJ after base", 'a', ClusterZWJ, true},
		{"ZWJ before base", ClusterZWJ, 'a', true},
		{"combining mark after base", 'e', 0x0301, true},
		{"combining mark before base", 0x0301, 'a', false},
		{"variation selector", 0x2764, 0xFE0F, true},
		{"skin tone", 0x1F44D, 0x1F3FD, true},
		{"emoji tag char", 0x1F3F4, 0xE0067, true},
		{"ZWNJ after base", 'e', ClusterZWNJ, true},
		{"regional indicators", RIStart, RIStart + 1, true},
		{"single regional indicator then ascii", RIStart, 'a', false},
		// Glues 接收 rune ID，jamo 类别由 HangulClass 在内部推导。
		{"jamo L then V", 0x1100, 0x1161, true},
		{"jamo L then T", 0x1100, 0x11A8, false},
		{"jamo LV then T", 0xAC00, 0x11A8, true},
		{"jamo T then T", 0x11A8, 0x11A8, true},
		{"ascii pair", 'a', 'b', false},
		{"invalid prev never glues", InvalidRuneID, 0x0301, false},
		{"invalid curr never glues", 'e', InvalidRuneID, false},
		{"both invalid", InvalidRuneID, InvalidRuneID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Glues(tc.prev, tc.cur); got != tc.want {
				t.Errorf("Glues(U+%04X, U+%04X) = %v, want %v", tc.prev, tc.cur, got, tc.want)
			}
		})
	}
}

// TestHangulGluesClasses 直接覆盖 GB6–8 的 jamo 类别组合表。
func TestHangulGluesClasses(t *testing.T) {
	if got := HangulClass(0x1100); got != JamoL {
		t.Errorf("HangulClass(0x1100) = %d, want JamoL", got)
	}
	if got := HangulClass(0x1161); got != JamoV {
		t.Errorf("HangulClass(0x1161) = %d, want JamoV", got)
	}
	if got := HangulClass(0x11A8); got != JamoT {
		t.Errorf("HangulClass(0x11A8) = %d, want JamoT", got)
	}
	if got := HangulClass(0xAC00); got != JamoLV {
		t.Errorf("HangulClass(0xAC00) = %d, want JamoLV", got)
	}
	if got := HangulClass(0xAC01); got != JamoLVT {
		t.Errorf("HangulClass(0xAC01) = %d, want JamoLVT", got)
	}
	if got := HangulClass('a'); got != JamoNone {
		t.Errorf("HangulClass('a') = %d, want JamoNone", got)
	}

	cases := []struct {
		prev, next JamoClass
		want       bool
	}{
		{JamoL, JamoL, true},
		{JamoL, JamoV, true},
		{JamoL, JamoLV, true},
		{JamoL, JamoLVT, true},
		{JamoL, JamoT, false},
		{JamoLV, JamoV, true},
		{JamoLV, JamoT, true},
		{JamoV, JamoT, true},
		{JamoLVT, JamoT, true},
		{JamoLVT, JamoV, false},
		{JamoNone, JamoV, false},
	}
	for _, tc := range cases {
		if got := HangulGlues(tc.prev, tc.next); got != tc.want {
			t.Errorf("HangulGlues(%d, %d) = %v, want %v", tc.prev, tc.next, got, tc.want)
		}
	}
}

// refClusterBoundaries 是与 ClusterBoundaryBefore/After 独立的前向
// 扫描实现：它只用 ClusterRune/Glues 逐簇推进，给出所有簇边界。
func refClusterBoundaries(s string) []int {
	ends := make([]int, 0, len(s)+1)
	ends = append(ends, 0)
	for i := 0; i < len(s); {
		r, size := DecodeRuneAt(s, i)
		if size <= 0 {
			size = 1
		}
		j := i + size
		prev := ClusterRune(r, size)
		for j < len(s) {
			cr, cs := DecodeRuneAt(s, j)
			if cs <= 0 {
				break
			}
			curr := ClusterRune(cr, cs)
			if !Glues(prev, curr) {
				break
			}
			j += cs
			prev = curr
		}
		ends = append(ends, j)
		i = j
	}
	return ends
}

// TestClusterBoundariesMatchForwardScan 对每种多 rune 字素簇遍历
// 所有字节偏移，确认边界函数与独立前向扫描一致：before 只回退到
// <= at 的最近簇边界，after 只前进到 >= at 的最近簇边界；两者幂等，
// 且切出的片段逐字节拼回原串。
func TestClusterBoundariesMatchForwardScan(t *testing.T) {
	samples := []struct {
		name    string
		cluster string
	}{
		{"combining mark", "e\u0301"},
		{"ZWJ family", "👨‍👩‍👧"},
		{"regional indicator flag", "🇨🇳"},
		{"emoji skin tone", "👍🏽"},
		{"hangul jamo", "각"},
		{"CRLF", "a\r\n"},
		{"variation selector", "❤️"},
		{"emoji tag flag", "🏴\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F"},
		{"invalid bytes interleaved", "a\xFF中\xFE\u0301b"},
	}
	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Repeat(tc.cluster, 6) + "0123456789"
			ends := refClusterBoundaries(text)
			if ends[len(ends)-1] != len(text) {
				t.Fatalf("forward scan ended at %d, want %d", ends[len(ends)-1], len(text))
			}
			// 逐字节不变：按簇边界切片后拼回必须等于原串。
			var rebuilt strings.Builder
			for i := 0; i+1 < len(ends); i++ {
				rebuilt.WriteString(text[ends[i]:ends[i+1]])
			}
			if rebuilt.String() != text {
				t.Fatalf("cluster slices did not reassemble byte-for-byte")
			}

			for at := 0; at <= len(text); at++ {
				before := ClusterBoundaryBefore(text, at)
				after := ClusterBoundaryAfter(text, at)
				if !containsInt(ends, before) {
					t.Fatalf("before(%d) = %d is not a cluster boundary", at, before)
				}
				if !containsInt(ends, after) {
					t.Fatalf("after(%d) = %d is not a cluster boundary", at, after)
				}
				if before > at || after < at {
					t.Fatalf("direction broken at %d: before=%d after=%d", at, before, after)
				}
				// at 本身是边界时，两个函数都应停在 at。
				if containsInt(ends, at) && (before != at || after != at) {
					t.Fatalf("at %d is a boundary but before=%d after=%d", at, before, after)
				}
				// 幂等性：对结果再求一次边界不应改变它。
				if got := ClusterBoundaryBefore(text, before); got != before {
					t.Fatalf("before not idempotent at %d: %d -> %d", at, before, got)
				}
				if got := ClusterBoundaryAfter(text, after); got != after {
					t.Fatalf("after not idempotent at %d: %d -> %d", at, after, got)
				}
				// 边界处不应仍处于某个簇内部（端点除外）。
				if before > 0 && before < len(text) && GluedAt(text, before) {
					t.Fatalf("before(%d) = %d is mid-cluster", at, before)
				}
				if after > 0 && after < len(text) && GluedAt(text, after) {
					t.Fatalf("after(%d) = %d is mid-cluster", at, after)
				}
			}
		})
	}
}

// TestGluedAt 覆盖非法字节两侧、字符串端点与簇内部的粘合判定。
func TestGluedAt(t *testing.T) {
	cases := []struct {
		name string
		text string
		at   int
		want bool
	}{
		{"inside combining cluster", "e\u0301a", 1, true},
		{"between cluster and ascii", "e\u0301a", 3, false},
		{"inside ZWJ chain", "👨‍👩", 4, true},
		{"inside flag", "🇨🇳", 4, true},
		{"inside jamo", "각", 3, true},
		{"inside CRLF", "a\r\nb", 2, true},
		{"before invalid byte", "a\xFFb", 1, false},
		{"after invalid byte", "a\xFFb", 2, false},
		{"start", "e\u0301a", 0, false},
		{"end", "e\u0301a", 4, false},
		{"past end", "e\u0301a", 9, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GluedAt(tc.text, tc.at); got != tc.want {
				t.Errorf("GluedAt(%q, %d) = %v, want %v", tc.text, tc.at, got, tc.want)
			}
		})
	}
}

// TestTrimPartialRuneBacksOffToRuneStart 覆盖把字节偏移回退到
// rune 起点的逻辑；非法字节按单字节 rune 处理。
func TestTrimPartialRuneBacksOffToRuneStart(t *testing.T) {
	const chinese = "中文abc" // 中=0..2, 文=3..5, a=6, b=7, c=8 (len 9)
	const emoji = "a😀b"     // a=0, 😀=1..4, b=5 (len 6)
	cases := []struct {
		name   string
		text   string
		offset int
		want   int
	}{
		{"zero", chinese, 0, 0},
		{"negative", chinese, -1, 0},
		{"at rune start", chinese, 3, 3},
		{"inside first rune at one", chinese, 1, 0},
		{"inside first rune at two", chinese, 2, 0},
		{"inside second rune", chinese, 4, 3},
		{"ascii byte is a boundary", chinese, 6, 6},
		{"inside emoji rune", emoji, 2, 1},
		{"at emoji rune start", emoji, 1, 1},
		{"at end of text", chinese, len(chinese), len(chinese)},
		{"past end of text", chinese, 20, 20},
		{"invalid byte is its own rune", "a\xFFb", 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrimPartialRune(tc.text, tc.offset); got != tc.want {
				t.Fatalf("TrimPartialRune(%q, %d) = %d, want %d", tc.text, tc.offset, got, tc.want)
			}
		})
	}
}
