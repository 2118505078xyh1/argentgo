package tools

import (
	"strings"
	"testing"
)

// 本文件为 clipTo 的字素簇对齐行为添加专项测试，并验证
// "单 rune 字素" 样本仍与旧的 rune 参考逐字节一致。

// containsInt 报告 xs 是否包含 v。
func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// assertHeadTailClusterSafe 检查裁剪两端都落在完整字素簇边界上：
// head 不以孤立 extend/ZWJ/RI 结尾，tail 不以 extend/ZWJ 开头，
// 并且头尾起点都出现在参考实现给出的簇边界列表中。
func assertHeadTailClusterSafe(t *testing.T, text, head, tail string) {
	t.Helper()
	ends := graphemeEnds(text)
	if head != "" && !containsInt(ends, len(head)) {
		t.Fatalf("head ends mid-cluster at byte %d (boundaries %v): %q", len(head), ends, head)
	}
	tailStart := len(text) - len(tail)
	if tail != "" && !containsInt(ends, tailStart) {
		t.Fatalf("tail starts mid-cluster at byte %d (boundaries %v): %q", tailStart, ends, tail)
	}

	if head != "" {
		r, size := decodeLastRuneBefore(head, len(head))
		switch {
		case r == clusterZWJ:
			t.Fatalf("head ends with a dangling ZWJ: %q", head)
		case isRegionalIndicator(r):
			prevR, _ := decodeLastRuneBefore(head, len(head)-size)
			if !isRegionalIndicator(prevR) {
				t.Fatalf("head ends with an isolated regional indicator: %q", head)
			}
		case isClusterExtend(r):
			if !gluedAt(text, len(head)-size) {
				t.Fatalf("head ends with a base-less extend: %q", head)
			}
		}
	}
	if tail != "" {
		r, size := decodeRuneAt(tail, 0)
		if isClusterExtend(r) || r == clusterZWJ {
			t.Fatalf("tail starts with extend/ZWJ U+%04X: %q", r, tail)
		}
		if isRegionalIndicator(r) {
			nextR, _ := decodeRuneAt(tail, size)
			if !isRegionalIndicator(nextR) {
				t.Fatalf("tail starts with an isolated regional indicator: %q", tail)
			}
		}
	}
}

// TestClipToGraphemeBoundaries 在每种多 rune 字素簇上遍历所有限制，
// 使头尾偏移扫过字簇内部的每个字节位置，确认裁剪从不切在簇中间。
func TestClipToGraphemeBoundaries(t *testing.T) {
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
	}
	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Repeat(tc.cluster, 12) + "0123456789"
			for limit := 1; limit <= len(text)+2; limit++ {
				got := clipTo(text, limit)
				assertClipInvariants(t, text, limit, got)
				head, tail, truncated := splitClipped(t, got)
				if !truncated {
					continue
				}
				assertHeadTailClusterSafe(t, text, head, tail)
			}
		})
	}
}

// TestClusterBoundaryBeforeAfter 直接校验两个边界函数的方向性：
// before 只回退（<= at），after 只前进（>= at），且结果都是簇边界。
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterBoundaryBefore(tc.text, tc.at); got != tc.wantBefore {
				t.Errorf("clusterBoundaryBefore(%q, %d) = %d, want %d", tc.text, tc.at, got, tc.wantBefore)
			}
			if got := clusterBoundaryAfter(tc.text, tc.at); got != tc.wantAfter {
				t.Errorf("clusterBoundaryAfter(%q, %d) = %d, want %d", tc.text, tc.at, got, tc.wantAfter)
			}
		})
	}
}

// TestClipToMatchesRuneReferenceForSingleRuneGraphemes 确认：当每个
// rune 自身就是一个字簇时（ASCII/CJK/独立 emoji/非法字节），字素
// 裁剪与旧的按 rune 参考逐字节一致，不引入任何行为回归。
func TestClipToMatchesRuneReferenceForSingleRuneGraphemes(t *testing.T) {
	cases := []struct {
		name  string
		atom  string
		valid bool // 输入是否为合法 UTF-8
	}{
		{"ascii lower", "a", true},
		{"ascii digit", "0", true},
		{"cjk", "中", true},
		{"standalone emoji", "😀", true},
		{"invalid byte", "\xFF", false},
		{"two invalid bytes", "\xFF\xFE", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Repeat(tc.atom, 40)
			for limit := 1; limit <= len(text)+2; limit++ {
				got := clipTo(text, limit)
				if tc.valid {
					assertClipInvariants(t, text, limit, got)
				}
				head, tail, truncated := splitClipped(t, got)
				refHead, refTail, refTrunc := naiveClip(text, limit)
				if truncated != refTrunc || head != refHead || tail != refTail {
					t.Fatalf("clipTo(%q, %d) differs from rune reference:\n head got:  %q\n head want: %q\n tail got:  %q\n tail want: %q\n trunc got: %v want %v",
						tc.atom, limit, head, refHead, tail, refTail, truncated, refTrunc)
				}
			}
		})
	}
}
