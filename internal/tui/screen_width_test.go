package tui

import (
	"strings"
	"testing"
)

// Grapheme sequences used below. Escape literals keep the exact code points
// unambiguous regardless of how the source file is stored.
const (
	familyZWJ    = "\U0001F468\u200D\U0001F469\u200D\U0001F467"   // 👨‍👩‍👧
	coupleHeartZ = "\U0001F468\u200D\u2764\uFE0F\u200D\U0001F468" // 👨‍❤️‍👨
	flagCN       = "\U0001F1E8\U0001F1F3"                         // 🇨🇳
	thumbTone    = "\U0001F44D\U0001F3FD"                         // 👍🏽
	handTone     = "\u270B\U0001F3FD"                             // ✋🏽
	combiningE   = "e\u0301"                                      // é (decomposed)
	ci           = "\U0001F1E8"                                   // 🇨 (one regional indicator)
)

func TestTextWidthCountsGraphemeClusters(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"ZWJ family is one 2-cell cluster", familyZWJ, 2},
		{"ZWJ couple with variation selector is one cluster", coupleHeartZ, 2},
		{"flag pair is 2 cells", flagCN, 2},
		{"two flags are 4 cells", flagCN + flagCN, 4},
		{"a lone regional indicator stays 1 cell", ci, 1},
		{"combining mark does not add width", combiningE, 1},
		{"combining mark on a wide base", "你\u0301", 2},
		{"emoji plus skin tone modifier", thumbTone, 2},
		{"narrow base plus skin tone modifier takes max width", handTone, 2},
		{"CJK", "你好", 4},
		{"ASCII fast path", "hello world", 11},
		{"ASCII control bytes are zero width", "a\nb", 2},
		{"mixed scripts and clusters", "你好" + familyZWJ + flagCN, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TextWidth(tc.text); got != tc.want {
				t.Errorf("TextWidth(%q) = %d, want %d", tc.text, got, tc.want)
			}
		})
	}
}

func TestClipNeverSplitsAGraphemeCluster(t *testing.T) {
	cases := []struct {
		name string
		text string
		max  int
		want string
	}{
		{"a ZWJ family wider than the budget is dropped whole", familyZWJ, 1, ""},
		{"a ZWJ family fits when the budget is 2", familyZWJ, 2, familyZWJ},
		{"clip keeps ASCII then drops the following ZWJ cluster", "a" + familyZWJ, 2, "a"},
		{"half a flag is never returned", flagCN, 1, ""},
		{"flag and ASCII both fit", flagCN + "x", 3, flagCN + "x"},
		{"combining mark stays with its base", combiningE + "x", 1, combiningE},
		{"skin tone modifier stays with its emoji", thumbTone, 2, thumbTone},
		{"existing wide-rune behaviour is unchanged", "你好世界", 4, "你好"},
		{"existing ASCII behaviour is unchanged", "abcdef", 3, "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Clip(tc.text, tc.max)
			if got != tc.want {
				t.Errorf("Clip(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
			}
			if w := TextWidth(got); w > tc.max {
				t.Errorf("Clip(%q, %d) = %q with width %d > %d", tc.text, tc.max, got, w, tc.max)
			}
		})
	}
}

func TestWrapBreaksOnlyOnGraphemeBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{"ZWJ family moves to the next line intact", familyZWJ + "x", 2, []string{familyZWJ, "x"}},
		{"flags are never folded in half", flagCN + "x" + flagCN, 2, []string{flagCN, "x", flagCN}},
		{"combining sequence stays together", combiningE + "x", 1, []string{combiningE, "x"}},
		{"skin tone modifier stays with its emoji", thumbTone + "ab", 2, []string{thumbTone, "ab"}},
		{"CJK and ZWJ clusters", "你好" + familyZWJ, 4, []string{"你好", familyZWJ}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Wrap(tc.text, tc.width)
			if len(got) != len(tc.want) {
				t.Fatalf("Wrap(%q, %d) = %q, want %q", tc.text, tc.width, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("line %d = %q, want %q (full: %q)", i, got[i], tc.want[i], got)
				}
				if w := TextWidth(got[i]); w > tc.width {
					t.Errorf("line %q is %d cells wide, want <= %d", got[i], w, tc.width)
				}
			}
			if joined := strings.Join(got, ""); joined != tc.text {
				t.Errorf("joined lines = %q, want %q", joined, tc.text)
			}
		})
	}
}

func TestGraphemeFixChangesZWJWidth(t *testing.T) {
	// The old implementation summed RuneWidth per rune (the loop below), which
	// measured a ZWJ family as six cells and could cut it in half. The new
	// cluster-aware TextWidth measures two. Kept as documentation of the
	// regression so a future refactor cannot silently reintroduce it.
	legacy := func(s string) int {
		total := 0
		for _, r := range s {
			total += RuneWidth(r)
		}
		return total
	}
	if legacy(familyZWJ) != 6 {
		t.Fatalf("unexpected legacy width %d for %q", legacy(familyZWJ), familyZWJ)
	}
	if got := TextWidth(familyZWJ); got != 2 {
		t.Errorf("TextWidth(ZWJ family) = %d, want 2", got)
	}
	if got := Clip(familyZWJ, 1); got != "" {
		t.Errorf("old code would return half a family, new code returns %q, want empty", got)
	}
}
