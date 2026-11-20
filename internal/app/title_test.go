package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTitleFromPromptFallbackTruncatesAtLimitWithEllipsis(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		limit  int
		want   string
	}{
		{"within limit", "hello world", 20, "hello world"},
		{"exact limit", "hello world", 11, "hello world"},
		{"over limit", "hello world", 5, "hello…"},
		{"single rune", strings.Repeat("字", 8), 1, "字…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleFromPromptFallback(tc.prompt, tc.limit); got != tc.want {
				t.Fatalf("titleFromPromptFallback(%q, %d) = %q, want %q", tc.prompt, tc.limit, got, tc.want)
			}
		})
	}
	// 截断后内容恰好为 limit 个符文加一个省略号。
	if got := titleFromPromptFallback(strings.Repeat("界", 90), 60); got != strings.Repeat("界", 60)+"…" {
		t.Fatalf("long title = %q", got)
	}
}

func TestTitleFromPromptFallbackCJKAndEmojiSafe(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		limit  int
	}{
		{"cjk", strings.Repeat("中文标题测试", 6), 7},
		{"emoji", strings.Repeat("🚀界", 12), 9},
		{"mixed", "fix 修复 🚀 bug 问题", 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := titleFromPromptFallback(tc.prompt, tc.limit)
			if !utf8.ValidString(got) {
				t.Fatalf("result %q is not valid UTF-8", got)
			}
			if r := []rune(got); len(r) != tc.limit+1 {
				t.Fatalf("result %q has %d runes, want %d", got, len(r), tc.limit+1)
			}
		})
	}
}

func TestTitleFromPromptFallbackEmptyReturnsDefault(t *testing.T) {
	for _, prompt := range []string{"", "   ", "\n\t\r", " \x00\x1b "} {
		if got := titleFromPromptFallback(prompt, 60); got != defaultTitleName {
			t.Fatalf("titleFromPromptFallback(%q) = %q, want %q", prompt, got, defaultTitleName)
		}
	}
	if got := titleFromPromptFallback("hello", 0); got != defaultTitleName {
		t.Fatalf("non-positive limit = %q, want %q", got, defaultTitleName)
	}
}

func TestTitleFromPromptFallbackStripsControlCharsAndCollapsesWhitespace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  布置  任务\n下一行 ", "布置 任务 下一行"},
		{"a\x00b\x1fc\td", "a b c d"},
		{"第一行\r\n第二行\u0085第三行", "第一行 第二行 第三行"},
	}
	for _, tc := range cases {
		if got := titleFromPromptFallback(tc.in, 60); got != tc.want {
			t.Fatalf("titleFromPromptFallback(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShouldGenerateTitleOnlyForPlaceholderAndLongPrompt(t *testing.T) {
	long := strings.Repeat("字", titlePromptMinRunes)
	short := strings.Repeat("字", titlePromptMinRunes-1)
	cases := []struct {
		name         string
		prompt       string
		currentTitle string
		want         bool
	}{
		{"empty title and long prompt", long, "", true},
		{"default chinese name", long, defaultTitleName, true},
		{"default with spaces", long, "  " + defaultTitleName + "  ", true},
		{"legacy session name", long, "session", true},
		{"empty title but short prompt", short, "", false},
		{"default title but short prompt", short, defaultTitleName, false},
		{"custom title never regenerates", long, "我的自定义标题", false},
		{"explicit english title never regenerates", long, "bug-1234", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldGenerateTitle(tc.prompt, tc.currentTitle); got != tc.want {
				t.Fatalf("shouldGenerateTitle(%q, %q) = %v, want %v", tc.prompt, tc.currentTitle, got, tc.want)
			}
		})
	}
}

func TestShouldGenerateTitleUsesCleanedLength(t *testing.T) {
	// 大量空白与控制字符应被折叠，不应被当作有效内容计入阈值。
	padded := "字" + strings.Repeat("\n", 50)
	if shouldGenerateTitle(padded, defaultTitleName) {
		t.Fatal("padded short prompt should not trigger generation")
	}
	// 折叠后仍达到阈值时正常触发。
	padded = strings.Repeat("字", titlePromptMinRunes) + strings.Repeat("\x00", 50)
	if !shouldGenerateTitle(padded, defaultTitleName) {
		t.Fatal("padded long prompt should trigger generation")
	}
}
