package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// countClusters 遍历包产生的字簇边界。它
// 只用于"从不超过上限"这一属性；下面具体的预期字符串
// 都以字面写出，这样 clusterEnd 中的 bug 无法在那里藏身。
func countClusters(s string) int {
	n := 0
	for i := 0; i < len(s); {
		i = clusterEnd(s, i)
		n++
	}
	return n
}

func TestClipGraphemesASCII(t *testing.T) {
	s := "hello world"
	if got := ClipGraphemes(s, 100); got != s {
		t.Fatalf("clip to 100 = %q, want %q", got, s)
	}
	if got := ClipGraphemes(s, 5); got != "hello" {
		t.Fatalf("clip to 5 = %q, want %q", got, "hello")
	}
	if got := ClipGraphemes(s, len(s)); got != s {
		t.Fatalf("clip to exact length = %q, want %q", got, s)
	}
}

func TestClipGraphemesCJK(t *testing.T) {
	s := "你好世界"
	if got := ClipGraphemes(s, 2); got != "你好" {
		t.Fatalf("clip CJK to 2 = %q, want %q", got, "你好")
	}
	if got := ClipGraphemes(s, 1); got != "你" {
		t.Fatalf("clip CJK to 1 = %q, want %q", got, "你")
	}
	if got := ClipGraphemes(s, 4); got != s {
		t.Fatalf("clip CJK to 4 = %q, want %q", got, s)
	}
}

func TestClipGraphemesEmoji(t *testing.T) {
	s := "🙂🚀" // two single-codepoint emoji
	if got := ClipGraphemes(s, 1); got != "🙂" {
		t.Fatalf("clip emoji to 1 = %q, want %q", got, "🙂")
	}
	if got := ClipGraphemes(s, 2); got != s {
		t.Fatalf("clip emoji to 2 = %q, want %q", got, s)
	}
}

func TestClipGraphemesKeepsCombiningSequence(t *testing.T) {
	s := "e\u0301x" // e + combining acute + x
	if got := ClipGraphemes(s, 1); got != "e\u0301" {
		t.Fatalf("clip combining to 1 = %q, want %q", got, "e\u0301")
	}
	if got := ClipGraphemes(s, 2); got != s {
		t.Fatalf("clip combining to 2 = %q, want %q", got, s)
	}
}

func TestClipGraphemesKeepsZWJFamily(t *testing.T) {
	family := "👨\u200d👩\u200d👧\u200d👦"
	if n := countClusters(family); n != 1 {
		t.Fatalf("family cluster count = %d, want 1", n)
	}
	if got := ClipGraphemes(family, 1); got != family {
		t.Fatalf("clip ZWJ family to 1 = %q, want %q", got, family)
	}
	s := "a" + family + "b"
	if got := ClipGraphemes(s, 2); got != "a"+family {
		t.Fatalf("clip around ZWJ family = %q, want %q", got, "a"+family)
	}
}

func TestClipGraphemesKeepsRegionalFlagPair(t *testing.T) {
	flag := "\U0001F1E8\U0001F1F3" // regional indicators C + N
	if n := countClusters(flag); n != 1 {
		t.Fatalf("flag cluster count = %d, want 1", n)
	}
	if got := ClipGraphemes(flag, 1); got != flag {
		t.Fatalf("clip flag to 1 = %q, want %q", got, flag)
	}
	two := flag + "\U0001F1EF\U0001F1F5" // + Japan
	if got := ClipGraphemes(two, 1); got != flag {
		t.Fatalf("clip two flags to 1 = %q, want %q", got, flag)
	}
	if got := ClipGraphemes(two, 2); got != two {
		t.Fatalf("clip two flags to 2 = %q, want %q", got, two)
	}
}

func TestClipGraphemesKeepsHangulJamo(t *testing.T) {
	// L + V + T jamo 组成一个音节字簇。
	s := "\u1100\u1161\u11A8"
	if n := countClusters(s); n != 1 {
		t.Fatalf("jamo cluster count = %d, want 1", n)
	}
	if got := ClipGraphemes(s, 1); got != s {
		t.Fatalf("clip jamo to 1 = %q, want %q", got, s)
	}
}

func TestClipGraphemesNonPositiveLimit(t *testing.T) {
	for _, limit := range []int{0, -1, -100} {
		if got := ClipGraphemes("hello", limit); got != "" {
			t.Fatalf("clip to %d = %q, want empty", limit, got)
		}
	}
}

func TestClipGraphemesAlwaysValidUTF8AndWithinCap(t *testing.T) {
	cases := []string{
		"plain ascii",
		"你好世界！🙂",
		"e\u0301x 🚀\U0001F1E8\U0001F1F3",
		"👨\u200d👩\u200d👧\u200d👦 end",
		"\u1100\u1161\u11A8가각",
		"a\xffb", // deliberately malformed UTF-8
	}
	for _, s := range cases {
		for cap := -1; cap <= countClusters(s)+2; cap++ {
			got := ClipGraphemes(s, cap)
			if !utf8.ValidString(got) {
				t.Fatalf("ClipGraphemes(%q, %d) = invalid UTF-8 %q", s, cap, got)
			}
			if cap > 0 && countClusters(got) > cap {
				t.Fatalf("ClipGraphemes(%q, %d) = %q with %d clusters, cap %d",
					s, cap, got, countClusters(got), cap)
			}
			if len(got) > len(s) {
				t.Fatalf("clip grew the string: %q -> %q", s, got)
			}
		}
	}
}

func TestClipGraphemesIsDeterministic(t *testing.T) {
	s := strings.Repeat("a👨\u200d👩e\u0301\U0001F1E8\U0001F1F3你", 200)
	want := ClipGraphemes(s, 137)
	for i := 0; i < 1000; i++ {
		if got := ClipGraphemes(s, 137); got != want {
			t.Fatalf("call %d = %q, want %q", i, got, want)
		}
	}
}

func TestTruncateGraphemesAppendsEllipsis(t *testing.T) {
	if got := TruncateGraphemes("abcdef", 3); got != "abc…" {
		t.Fatalf("truncate ascii = %q, want %q", got, "abc…")
	}
	if got := TruncateGraphemes("abc", 3); got != "abc" {
		t.Fatalf("truncate fitting string = %q, want unchanged %q", got, "abc")
	}
	if got := TruncateGraphemes("e\u0301x", 1); got != "e\u0301…" {
		t.Fatalf("truncate combining = %q, want %q", got, "e\u0301…")
	}
	if got := TruncateGraphemes("🙂🚀", 1); got != "🙂…" {
		t.Fatalf("truncate emoji = %q, want %q", got, "🙂…")
	}
}

func TestClipGraphemeBytesRespectsClusterBoundaries(t *testing.T) {
	cases := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"ascii", "abcdef", 3, "abc"},
		{"combining fits", "e\u0301x", 3, "e\u0301"},
		{"combining does not fit", "e\u0301x", 2, ""},
		{"four byte emoji does not fit", "🙂x", 3, ""},
		{"four byte emoji fits", "🙂x", 4, "🙂"},
		{"flag", "\U0001F1E8\U0001F1F3x", 8, "\U0001F1E8\U0001F1F3"},
		{"zero budget", "abc", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clipGraphemeBytes(tc.s, tc.max)
			if got != tc.want {
				t.Fatalf("clipGraphemeBytes(%q, %d) = %q, want %q", tc.s, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
		})
	}
}
