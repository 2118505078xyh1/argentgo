package session

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSnippetForCaseFoldKeepsRuneAlignment 覆盖 session 审计 #7。小写化
// 首字符，如 İ（2→1 字节）、开尔文符号（3→1）或 ẞ（3→2），
// 会缩短字节串，因此用小写化后的字节偏移切分
// 原始值会推移片段窗口。符文偏移是稳定的，因为
// strings.ToLower 按符文逐一折叠。
func TestSnippetForCaseFoldKeepsRuneAlignment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
	}{
		{"dotted-capital-i", "İ"}, // U+0130 -> i：2 字节 -> 1
		{"kelvin-sign", "\u212a"}, // U+212A -> k：3 字节 -> 1
		{"capital-sharp-s", "ẞ"},  // U+1E9E -> ß：3 字节 -> 2
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 开头一个被折叠的符文，60 个 x，needle，然后足够多的 y，
			// 使 96 符文的尾部被完整生成（并被省略）。
			value := tc.prefix + strings.Repeat("x", 60) + "needle" + strings.Repeat("y", 200)
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			snippet, ok := snippetFor(data, "needle")
			if !ok {
				t.Fatal("snippetFor found no hit")
			}
			want := "…" + strings.Repeat("x", 48) + "needle" + strings.Repeat("y", 96) + "…"
			if snippet != want {
				t.Fatalf("snippet = %q (%d runes), want %q (%d runes)",
					snippet, utf8.RuneCountInString(snippet), want, utf8.RuneCountInString(want))
			}
		})
	}
}

// TestSnippetForFoldedNeedle 检查对称情形：needle 本身
// 包含一个小写形式字节长度不同的符文。
func TestSnippetForFoldedNeedle(t *testing.T) {
	value := "prefix " + "İSTANBUL" + " suffix"
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	snippet, ok := snippetFor(data, "istanbul")
	if !ok {
		t.Fatal("snippetFor did not match İSTANBUL with a lowercase needle")
	}
	if !strings.Contains(snippet, "İSTANBUL") {
		t.Fatalf("snippet = %q, want the original (unlowered) text", snippet)
	}
}
