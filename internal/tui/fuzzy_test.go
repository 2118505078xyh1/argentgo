package tui

import (
	"reflect"
	"testing"
)

func checkMatch(t *testing.T, pattern, text string, pathMode, wantOK bool, wantPos []int) int {
	t.Helper()
	score, positions, ok := FuzzyMatch(pattern, text, pathMode)
	if ok != wantOK {
		t.Fatalf("FuzzyMatch(%q, %q, %v) ok = %v, want %v", pattern, text, pathMode, ok, wantOK)
	}
	if !wantOK {
		if score != 0 || positions != nil {
			t.Fatalf("FuzzyMatch(%q, %q) miss returned score=%d positions=%v, want 0/nil", pattern, text, score, positions)
		}
		return score
	}
	if wantPos != nil && !reflect.DeepEqual(positions, wantPos) {
		t.Fatalf("FuzzyMatch(%q, %q) positions = %v, want %v", pattern, text, positions, wantPos)
	}
	return score
}

func TestFuzzyMatchBasic(t *testing.T) {
	score := checkMatch(t, "abc", "abc", false, true, []int{0, 1, 2})
	if score <= 0 {
		t.Fatalf("exact match should have a positive score, got %d", score)
	}
	checkMatch(t, "ac", "abc", false, true, []int{0, 2})
	checkMatch(t, "abc", "xxaybzc", false, true, []int{2, 4, 6})
}

func TestFuzzyMatchSmartCase(t *testing.T) {
	// 模式中没有大写 -> 不区分大小写。
	checkMatch(t, "abc", "aBc", false, true, []int{0, 1, 2})
	checkMatch(t, "abc", "xxABC", false, true, []int{2, 3, 4})
	// 模式中有大写 -> 区分大小写。
	checkMatch(t, "AbC", "xxAbCyy", false, true, []int{2, 3, 4})
	checkMatch(t, "AbC", "abc", false, false, nil)
	checkMatch(t, "HTTP", "http", false, false, nil)
	checkMatch(t, "HTTP", "xHTTPy", false, true, []int{1, 2, 3, 4})
}

func TestFuzzyMatchWordBoundaryBonus(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		with    string
		without string
	}{
		{"separator", "ab", "hello_ab", "helloab"},
		{"camel", "ab", "fooAb", "fooab"},
		{"digit", "ab", "v2ab", "vvab"},
		{"space", "ab", "hi ab", "hiab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			with := checkMatch(t, tc.pattern, tc.with, false, true, nil)
			without := checkMatch(t, tc.pattern, tc.without, false, true, nil)
			if with <= without {
				t.Fatalf("boundary match %q scored %d, want > plain match %q score %d", tc.with, with, tc.without, without)
			}
		})
	}
}

func TestFuzzyMatchConsecutiveBonus(t *testing.T) {
	consecutive := checkMatch(t, "ab", "ab", false, true, []int{0, 1})
	scattered := checkMatch(t, "ab", "aXb", false, true, []int{0, 2})
	if consecutive <= scattered {
		t.Fatalf("consecutive match scored %d, want > scattered score %d", consecutive, scattered)
	}
}

func TestFuzzyMatchGapPenalty(t *testing.T) {
	tight := checkMatch(t, "ab", "ab", false, true, nil)
	smallGap := checkMatch(t, "ab", "aXXXb", false, true, nil)
	largeGap := checkMatch(t, "ab", "aXXXXXXb", false, true, nil)
	if !(tight > smallGap && smallGap > largeGap) {
		t.Fatalf("gap penalty not monotonic: tight=%d small=%d large=%d", tight, smallGap, largeGap)
	}
	// 前导间隔同样扣分。
	leading := checkMatch(t, "ab", "ZZab", false, true, nil)
	if leading >= tight {
		t.Fatalf("leading gap scored %d, want < tight score %d", leading, tight)
	}
}

func TestFuzzyMatchPathMode(t *testing.T) {
	score, positions, ok := FuzzyMatch("src/util", "src/util.go", true)
	if !ok || !reflect.DeepEqual(positions, []int{0, 1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("path match ok=%v positions=%v", ok, positions)
	}
	plainScore, plainPositions, plainOK := FuzzyMatch("src/util", "src/util.go", false)
	if !plainOK || !reflect.DeepEqual(plainPositions, positions) {
		t.Fatalf("plain match should find the same runes: ok=%v positions=%v", plainOK, plainPositions)
	}
	if score <= plainScore {
		t.Fatalf("pathMode score %d should exceed plain score %d", score, plainScore)
	}

	// ':' 仅在 pathMode 下是边界。
	pathScore := checkMatch(t, "bc", "a:bcd", true, true, nil)
	normalScore := checkMatch(t, "bc", "a:bcd", false, true, nil)
	if pathScore <= normalScore {
		t.Fatalf("':' boundary: pathMode %d should exceed plain %d", pathScore, normalScore)
	}
}

func TestFuzzyMatchCJKAndEmoji(t *testing.T) {
	text := "你好世界abc"
	score := checkMatch(t, "好世", text, false, true, []int{1, 2})
	if score <= 0 {
		t.Fatalf("CJK match should score > 0, got %d", score)
	}
	runes := []rune(text)
	if string(runes[1]) != "好" || string(runes[2]) != "世" {
		t.Fatalf("positions are not rune indices into the original text")
	}

	// Emoji 是单个 rune；位置必须按 rune 计，而非 UTF-8 字节。
	checkMatch(t, "🚀X", "a🚀bXc", false, true, []int{1, 3})
	checkMatch(t, "界🚀", "世界🚀!", false, true, []int{1, 2})
}

func TestFuzzyMatchNoMatch(t *testing.T) {
	checkMatch(t, "xyz", "abc", false, false, nil)
	checkMatch(t, "ba", "ab", false, false, nil) // out of order
	checkMatch(t, "aaaa", "aa", false, false, nil)
	checkMatch(t, "A", "a", false, false, nil) // case-sensitive miss
}

func TestFuzzyMatchEmptyPattern(t *testing.T) {
	score, positions, ok := FuzzyMatch("", "anything", false)
	if !ok || score != 0 || len(positions) != 0 {
		t.Fatalf("empty pattern: ok=%v score=%d positions=%v", ok, score, positions)
	}
}

func TestSortFuzzyResultsDeterministic(t *testing.T) {
	results := []FuzzyResult{
		{Score: 10, TextLen: 5, Positions: []int{0}, Index: 0},  // A: longest
		{Score: 20, TextLen: 10, Positions: []int{3}, Index: 1}, // B: top score
		{Score: 10, TextLen: 3, Positions: []int{2}, Index: 2},  // C: later start
		{Score: 10, TextLen: 3, Positions: []int{1}, Index: 3},  // D: earlier start
		{Score: 10, TextLen: 3, Positions: []int{1}, Index: 0},  // E: full tie with D, lower Index
	}
	SortFuzzyResults(results)
	wantIndexOrder := []int{1, 0, 3, 2, 0}
	got := make([]int, len(results))
	for i, r := range results {
		got[i] = r.Index
	}
	if !reflect.DeepEqual(got, wantIndexOrder) {
		t.Fatalf("sorted Index order = %v, want %v", got, wantIndexOrder)
	}

	// 完全相同的条目保持原有顺序（稳定）。
	equal := []FuzzyResult{
		{Score: 5, TextLen: 2, Positions: []int{0}, Index: 7},
		{Score: 5, TextLen: 2, Positions: []int{0}, Index: 7},
	}
	SortFuzzyResults(equal)
	if equal[0].Index != 7 || equal[1].Index != 7 {
		t.Fatalf("stable sort changed identical entries")
	}

	SortFuzzyResults(nil)
}
