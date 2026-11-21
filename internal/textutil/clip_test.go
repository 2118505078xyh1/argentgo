package textutil

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

// clipMin 返回两数较小值，仅测试内部使用。
func clipMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestClipRunesGoldenNoClusters 是默认路径的逐字节同一性 golden：
// 对不含多 rune 字素簇的输入（ASCII / 纯汉字 / 单独 emoji），
// ClipRunes 必须逐字节等于 string([]rune(s)[:limit])。limit 一路取到
// len(s)+2，覆盖 0、恰好、越界三种情况。
func TestClipRunesGoldenNoClusters(t *testing.T) {
	samples := []string{"", "abc", "aZ9", "中文汉字", "😀😀😀"}
	for _, s := range samples {
		runes := []rune(s)
		for limit := 0; limit <= len(s)+2; limit++ {
			want := string(runes[:clipMin(limit, len(runes))])
			if got := ClipRunes(s, limit); got != want {
				t.Fatalf("ClipRunes(%q, %d) = %q, want %q", s, limit, got, want)
			}
		}
	}
}

// clusterSamples 汇集各类多 rune 字素簇，供三个裁剪原语的测试共用。
var clusterSamples = []struct {
	name, text string
}{
	{"combining mark", "e\u0301a"},
	{"ZWJ family", "a👨‍👩‍👧‍👦b"},
	{"regional indicator flag", "a🇨🇳b"},
	{"emoji skin tone", "a👍🏽b"},
	{"hangul jamo", "a각b"},
	{"CRLF", "a\r\nb"},
	{"invalid byte", "a\xFF中b"},
	{"variation selector", "a❤️b"},
	{"emoji tag flag", "a🏴\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007Fb"},
}

// TestClipRunesClusterBoundary 验证：ClipRunes 只收缩、落在簇边界、
// 始终输出合法 UTF-8、RuneCount <= limit，且输出是重编码串的前缀，
// 长度不超过原实现 string([]rune[:limit])。
func TestClipRunesClusterBoundary(t *testing.T) {
	for _, tc := range clusterSamples {
		t.Run(tc.name, func(t *testing.T) {
			runes := []rune(tc.text)
			encoded := string(runes)
			n := len(runes)
			for limit := 0; limit <= n+2; limit++ {
				got := ClipRunes(tc.text, limit)
				wantDefault := string(runes[:clipMin(limit, n)])

				if !utf8.ValidString(got) {
					t.Fatalf("limit %d: output %q is not valid UTF-8", limit, got)
				}
				if rc := utf8.RuneCountInString(got); rc > clipMin(limit, n) {
					t.Fatalf("limit %d: RuneCount=%d exceeds budget %d", limit, rc, clipMin(limit, n))
				}
				if !strings.HasPrefix(encoded, got) {
					t.Fatalf("limit %d: %q is not a prefix of %q", limit, got, encoded)
				}
				if len(got) > len(wantDefault) {
					t.Fatalf("limit %d: len=%d exceeds default impl len=%d", limit, len(got), len(wantDefault))
				}
				// 终点恒为边界（构造使然）；更关键的是原串切点未劈开字簇。
				if GluedAt(got, len(got)) {
					t.Fatalf("limit %d: output end is mid-cluster", limit)
				}
				if len(got) > 0 && len(got) < len(encoded) && GluedAt(encoded, len(got)) {
					t.Fatalf("limit %d: cut at byte %d splits a cluster of %q", limit, len(got), encoded)
				}
				if again := ClipRunes(tc.text, limit); again != got {
					t.Fatalf("limit %d: not deterministic: %q then %q", limit, got, again)
				}
			}
		})
	}
}

// TestClipRunesTailClusterBoundary 验证尾部裁剪：输出是重编码串的后缀、
// 合法 UTF-8、RuneCount <= limit，起点吸附到簇边界且不会留下悬空附着符。
func TestClipRunesTailClusterBoundary(t *testing.T) {
	for _, tc := range clusterSamples {
		t.Run(tc.name, func(t *testing.T) {
			runes := []rune(tc.text)
			encoded := string(runes)
			n := len(runes)
			for limit := 0; limit <= n+2; limit++ {
				got := ClipRunesTail(tc.text, limit)
				budget := clipMin(limit, n)
				wantDefault := string(runes[n-budget:])

				if !utf8.ValidString(got) {
					t.Fatalf("limit %d: output %q is not valid UTF-8", limit, got)
				}
				if rc := utf8.RuneCountInString(got); rc > budget {
					t.Fatalf("limit %d: RuneCount=%d exceeds budget %d", limit, rc, budget)
				}
				if !strings.HasSuffix(encoded, got) {
					t.Fatalf("limit %d: %q is not a suffix of %q", limit, got, encoded)
				}
				if len(got) > len(wantDefault) {
					t.Fatalf("limit %d: len=%d exceeds default impl len=%d", limit, len(got), len(wantDefault))
				}
				cut := len(encoded) - len(got)
				if cut > 0 && cut < len(encoded) && GluedAt(encoded, cut) {
					t.Fatalf("limit %d: start cut at byte %d splits a cluster of %q", limit, cut, encoded)
				}
				if again := ClipRunesTail(tc.text, limit); again != got {
					t.Fatalf("limit %d: not deterministic: %q then %q", limit, got, again)
				}
			}
		})
	}
}

// TestClipWindowClusters 覆盖搜索高亮窗口：两侧吸附到簇边界，只收缩；
// 窗口完全落入单个字簇内部时返回空串。
func TestClipWindowClusters(t *testing.T) {
	cases := []struct {
		name          string
		s             string
		start, length int
		want          string
	}{
		{"ascii middle", "abcde", 1, 2, "bc"},
		{"ascii start", "abcde", 0, 2, "ab"},
		{"ascii past end", "abc", 5, 2, ""},
		{"ascii zero length", "abc", 1, 0, ""},
		{"ascii negative start clamps", "abc", -3, 2, "ab"},
		{"ascii length clamps", "abc", 1, 99, "bc"},
		{"combining whole cluster", "e\u0301x", 0, 2, "e\u0301"},
		{"combining partial start", "e\u0301x", 0, 1, ""},
		{"combining continuation only", "e\u0301x", 1, 1, ""},
		{"combining skip cluster", "e\u0301x", 1, 2, "x"},
		{"ZWJ whole family", "a👨‍👩‍👧‍👦b", 1, 7, "👨‍👩‍👧‍👦"},
		{"ZWJ inside family", "a👨‍👩‍👧‍👦b", 2, 1, ""},
		{"ZWJ leading ascii", "a👨‍👩‍👧‍👦b", 0, 1, "a"},
		{"flag whole", "a🇨🇳b", 1, 2, "🇨🇳"},
		{"flag half", "a🇨🇳b", 1, 1, ""},
		{"skin tone whole", "a👍🏽b", 1, 2, "👍🏽"},
		{"skin tone half", "a👍🏽b", 1, 1, ""},
		{"jamo whole", "a각b", 1, 3, "각"},
		{"jamo partial", "a각b", 1, 2, ""},
		{"crlf whole", "a\r\nb", 1, 2, "\r\n"},
		{"crlf half", "a\r\nb", 1, 1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClipWindow(tc.s, tc.start, tc.length)
			if got != tc.want {
				t.Fatalf("ClipWindow(%q, %d, %d) = %q, want %q", tc.s, tc.start, tc.length, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("output %q is not valid UTF-8", got)
			}
			if again := ClipWindow(tc.s, tc.start, tc.length); again != got {
				t.Fatalf("not deterministic: %q then %q", got, again)
			}
		})
	}
}

// TestClipPrimitivesDoNotMutateInput 确认三个原语都不写入入参的底层字节。
// Go 字符串不可变，这里用独立的 []byte 副本守护调用前后字节一致。
func TestClipPrimitivesDoNotMutateInput(t *testing.T) {
	const text = "a👨‍👩‍👧‍👦\r\nx"
	backing := []byte(text)
	original := append([]byte(nil), backing...)
	s := string(backing)

	_ = ClipRunes(s, 3)
	_ = ClipRunesTail(s, 3)
	_ = ClipWindow(s, 1, 4)

	if !bytes.Equal(backing, original) {
		t.Fatalf("input backing bytes mutated: got %v, want %v", backing, original)
	}
}

// TestClipBoundaryIdempotentAtBoundary 额外校验底层边界函数在“已是簇边界”
// 的偏移上返回原偏移（幂等），这正是默认路径逐字节不变的根因。
func TestClipBoundaryIdempotentAtBoundary(t *testing.T) {
	for _, tc := range clusterSamples {
		t.Run(tc.name, func(t *testing.T) {
			ends := refClusterBoundaries(tc.text)
			for _, at := range ends {
				if got := ClusterBoundaryBefore(tc.text, at); got != at {
					t.Fatalf("before(%d) = %d at a boundary", at, got)
				}
				if got := ClusterBoundaryAfter(tc.text, at); got != at {
					t.Fatalf("after(%d) = %d at a boundary", at, got)
				}
				if at > 0 && at < len(tc.text) && GluedAt(tc.text, at) {
					t.Fatalf("boundary %d is mid-cluster", at)
				}
			}
			// 任意偏移吸附后的结果再次吸附不应改变。
			for at := 0; at <= len(tc.text); at++ {
				before := ClusterBoundaryBefore(tc.text, at)
				if got := ClusterBoundaryBefore(tc.text, before); got != before {
					t.Fatalf("before not idempotent at %d: %d -> %d", at, before, got)
				}
				after := ClusterBoundaryAfter(tc.text, at)
				if got := ClusterBoundaryAfter(tc.text, after); got != after {
					t.Fatalf("after not idempotent at %d: %d -> %d", at, after, got)
				}
			}
		})
	}
}
