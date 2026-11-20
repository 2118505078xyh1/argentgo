package security

import "testing"

// TestWildcardMatchMalformedPatternNeverMatches 守护"格式错误的模式永不匹配"这一
// 文档化契约。'*' 前缀回退过去即使 path.Match 报告 ErrBadPattern 也会运行，因此像
// "[*" 这样的拼写错误仍会匹配所有以 "[" 开头的主体 —— 悄悄把规则变成非预期的
// allow。
func TestWildcardMatchMalformedPatternNeverMatches(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		subject string
	}{
		{"bad class with trailing star", "[*", "[anything"},
		{"bad class inside pattern with trailing star", "admin[*-*", "admin[*-host"},
		{"bad class matching the literal prefix", "[*", "["},
		{"unclosed class with trailing star", "a[?*", "a[?host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WildcardMatch(tc.pattern, tc.subject); got {
				t.Errorf("WildcardMatch(%q, %q) = true, want false for a malformed pattern", tc.pattern, tc.subject)
			}
		})
	}
}

// TestWildcardMatchWindowsSeparators 验证用 Windows 路径分隔符编写的规则能匹配它们
// 所命名的路径。path.Match 在每个平台上都把 '\' 当作转义符，因此没有规范化时，
// 像 "C:\Windows\System32" 这样的精确规则甚至匹配不上完全相同的主体，而
// "dir\file?.txt" 规则永远无法匹配 "dir\file1.txt"。
func TestWildcardMatchWindowsSeparators(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		subject string
		want    bool
	}{
		{"exact path matches itself", `C:\Windows\System32`, `C:\Windows\System32`, true},
		{"star within a windows segment", `dir\file?.txt`, `dir\file1.txt`, true},
		{"star suffix crosses windows separator", `read\*`, `read\file.txt`, true},
		{"forward slash pattern matches backslash subject", `read/*`, `read\file.txt`, true},
		{"backslash pattern matches forward slash subject", `read\*`, `read/file.txt`, true},
		{"non-matching windows path stays non-matching", `C:\Windows\*`, `D:\Data\file`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WildcardMatch(tc.pattern, tc.subject); got != tc.want {
				t.Errorf("WildcardMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
			}
		})
	}
}

// TestWildcardMatchValidPatternsUnchanged 是回归守护：格式错误和平台相关的修复不得
// 改变良构模式的行为，包括跨越分隔符的尾部 '*' 前缀回退。
func TestWildcardMatchValidPatternsUnchanged(t *testing.T) {
	cases := []struct {
		pattern string
		subject string
		want    bool
	}{
		{"read", "read", true},
		{"read", "readme", false},
		{"read*", "readme", true},
		{"read*", "read/file.txt", true},
		{"git push*", "git push --force", true},
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
		{"**", "a/b/c", true},
		{"file[12].txt", "file2.txt", true},
		{"file?.txt", "file1.txt", true},
		{"*", "", true},
	}
	for _, tc := range cases {
		if got := WildcardMatch(tc.pattern, tc.subject); got != tc.want {
			t.Errorf("WildcardMatch(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}
