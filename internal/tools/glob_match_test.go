package tools

import "testing"

// TestMatchGlobBasics 覆盖字面量、各通配符与转义的基础语义。
func TestMatchGlobBasics(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{"empty pattern matches empty name", "", "", true},
		{"empty pattern rejects nonempty name", "", "x", false},
		{"literal match", "abc", "abc", true},
		{"literal mismatch", "abc", "abd", false},
		{"literal longer name", "abc", "abcd", false},
		{"star matches any", "*", "anything", true},
		{"star matches empty", "*", "", true},
		{"prefix star", "a*", "abc", true},
		{"prefix star empty tail", "a*", "a", true},
		{"prefix star no match", "a*", "ba", false},
		{"suffix star", "*c", "abc", true},
		{"suffix star no match", "*c", "ab", false},
		{"middle star", "a*c", "abc", true},
		{"middle star many", "a*c", "abbbc", true},
		{"middle star absent", "a*c", "ac", true},
		{"middle star no match", "a*c", "ab", false},
		{"two stars", "a*b*c", "axbyc", true},
		{"two stars empty middle", "a*b*c", "abc", true},
		{"question single", "?", "x", true},
		{"question empty", "?", "", false},
		{"question two chars", "?", "xy", false},
		{"questions exact", "???", "abc", true},
		{"questions too few", "???", "ab", false},
		{"star and question", "a?c", "abc", true},
		{"star and question mismatch", "a?c", "ac", false},
		{"escaped star literal", `a\*c`, "a*c", true},
		{"escaped star not wildcard", `a\*c`, "abc", false},
		{"escaped question literal", `a\?c`, "a?c", true},
		{"escaped bracket literal", `a\[c`, "a[c", true},
		{"escaped backslash", `a\\c`, `a\c`, true},
		{"escaped backslash not single", `a\\c`, `a\\c`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchGlob(tc.pattern, tc.input); got != tc.want {
				t.Fatalf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
			}
		})
	}
}

// TestMatchGlobCharClass 覆盖字符类的枚举、范围、否定与转义。
func TestMatchGlobCharClass(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{"class single char", "[abc]", "b", true},
		{"class char outside", "[abc]", "d", false},
		{"class empty input", "[abc]", "", false},
		{"class two chars", "[abc]", "ab", false},
		{"range lowercase", "[a-z]", "m", true},
		{"range boundary low", "[a-z]", "a", true},
		{"range boundary high", "[a-z]", "z", true},
		{"range outside", "[a-z]", "A", false},
		{"range digits", "[0-9]", "7", true},
		{"range digits outside", "[0-9]", "x", false},
		{"mixed class", "[a-c0-9]", "5", true},
		{"mixed class letter", "[a-c0-9]", "b", true},
		{"mixed class outside", "[a-c0-9]", "d", false},
		{"negated class hit", "[!abc]", "d", true},
		{"negated class miss", "[!abc]", "a", false},
		{"negated range hit", "[!a-z]", "7", true},
		{"negated range miss", "[!a-z]", "m", false},
		{"literal closing bracket", "[]]", "]", true},
		{"literal dash at end", "[a-]", "-", true},
		{"literal dash at end letter", "[a-]", "a", true},
		{"escaped bracket in class", `[\]]`, "]", true},
		{"negated escaped bracket", `[!\]x]`, "a", true},
		{"negated escaped bracket miss", `[!\]x]`, "]", false},
		{"class combined with star", "file[0-9].txt", "file3.txt", true},
		{"class combined with star mismatch", "file[0-9].txt", "filex.txt", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchGlob(tc.pattern, tc.input); got != tc.want {
				t.Fatalf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
			}
		})
	}
}

// TestMatchGlobCJK 验证 CJK 按 rune 比较，一个汉字算一个字符。
func TestMatchGlobCJK(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{"cjk literal", "中文", "中文", true},
		{"cjk literal mismatch", "中文", "中英", false},
		{"question matches one han", "?", "中", true},
		{"question rejects two han", "?", "中文", false},
		{"two questions match two han", "??", "中文", true},
		{"star matches han", "中*", "中文测试", true},
		{"star in cjk", "*文", "中文", true},
		{"class of han", "[中英]", "中", true},
		{"class of han miss", "[中英]", "文", false},
		{"range of han", "[一-鿿]", "中", true},
		{"negated cjk class", "[!中]", "文", true},
		{"question does not split utf8", "?", "中", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchGlob(tc.pattern, tc.input); got != tc.want {
				t.Fatalf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
			}
		})
	}
}

// TestMatchGlobPathSeparator 验证通配符不跨越 '/'，但字面 '/' 可匹配。
func TestMatchGlobPathSeparator(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{"star stops at slash", "*", "a/b", false},
		{"prefix star stops at slash", "a*", "a/b", false},
		{"suffix star stops at slash", "*/b", "a/b", true},
		{"star each segment", "*/b", "x/y/b", false},
		{"literal slash matches", "a/b", "a/b", true},
		{"literal slash mismatch", "a/b", "ab", false},
		{"question stops at slash", "?", "/", false},
		{"question in segment", "a/?", "a/b", true},
		{"class stops at slash", "[a-z]", "/", false},
		{"negated class stops at slash", "[!/]", "/", false},
		{"class in segment", "a/[bc]", "a/b", true},
		{"double star is two stars", "a**b", "a/b", false},
		{"escaped slash literal", `a\/b`, "a/b", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchGlob(tc.pattern, tc.input); got != tc.want {
				t.Fatalf("MatchGlob(%q, %q) = %v, want %v", tc.pattern, tc.input, got, tc.want)
			}
		})
	}
}

// TestMatchGlobInvalidPatterns 验证无效模式一律返回 false。
func TestMatchGlobInvalidPatterns(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		input   string
	}{
		{"trailing backslash", `abc\`, "abc"},
		{"single backslash", `\`, "x"},
		{"unterminated class", "[abc", "a"},
		{"unterminated empty class", "[", "a"},
		{"unterminated negated class", "[!abc", "a"},
		{"empty class", "[]", "a"},
		{"empty negated class", "[!]", "a"},
		{"reversed range", "[z-a]", "m"},
		{"reversed range in negated class", "[!z-a]", "m"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchGlob(tc.pattern, tc.input); got {
				t.Fatalf("MatchGlob(%q, %q) = true for invalid pattern, want false", tc.pattern, tc.input)
			}
		})
	}
}

// TestMatchAny 覆盖多模式匹配与入参不被修改。
func TestMatchAny(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		input    string
		want     bool
	}{
		{"nil patterns", nil, "anything", false},
		{"empty patterns", []string{}, "anything", false},
		{"first matches", []string{"*.go", "*.md"}, "main.go", true},
		{"second matches", []string{"*.go", "*.md"}, "readme.md", true},
		{"none matches", []string{"*.go", "*.md"}, "main.txt", false},
		{"invalid pattern ignored", []string{`[`, "*.go"}, "main.go", true},
		{"only invalid patterns", []string{`[`, `abc\`}, "abc", false},
		{"empty pattern in list", []string{"", "x"}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.patterns...)
			got := MatchAny(tc.patterns, tc.input)
			if got != tc.want {
				t.Fatalf("MatchAny(%q, %q) = %v, want %v", tc.patterns, tc.input, got, tc.want)
			}
			for i := range before {
				if before[i] != tc.patterns[i] {
					t.Fatalf("MatchAny mutated patterns[%d]: %q -> %q", i, before[i], tc.patterns[i])
				}
			}
		})
	}
}

// TestGlobPatternInfo 验证通配符检测与合法性判断。
func TestGlobPatternInfo(t *testing.T) {
	cases := []struct {
		name         string
		pattern      string
		wantWildcard bool
		wantValid    bool
	}{
		{"plain literal", "abc", false, true},
		{"empty pattern", "", false, true},
		{"star", "a*", true, true},
		{"question", "a?", true, true},
		{"class", "[abc]", true, true},
		{"escaped star", `a\*`, false, true},
		{"escaped question", `a\?`, false, true},
		{"escaped bracket", `a\[`, false, true},
		{"escaped backslash", `a\\`, false, true},
		{"mix of escaped and wildcard", `\*a?`, true, true},
		{"trailing backslash", `abc\`, false, false},
		{"wildcard with trailing backslash", `*\`, true, false},
		{"unterminated class", "[abc", true, false},
		{"empty class", "[]", true, false},
		{"empty negated class", "[!]", true, false},
		{"reversed range", "[z-a]", true, false},
		{"escaped trailing in class", `[a\`, true, false},
		{"valid negated class", "[!a-z]", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotWildcard, gotValid := GlobPatternInfo(tc.pattern)
			if gotWildcard != tc.wantWildcard || gotValid != tc.wantValid {
				t.Fatalf("GlobPatternInfo(%q) = (%v, %v), want (%v, %v)",
					tc.pattern, gotWildcard, gotValid, tc.wantWildcard, tc.wantValid)
			}
		})
	}
}

// TestGlobMatchPurityAndStability 验证纯函数语义：不修改传入的切片，
// 以及对同一输入重复调用结果稳定一致。
func TestGlobMatchPurityAndStability(t *testing.T) {
	patterns := []string{"*.go", "file[0-9].txt", `a\*`, "中文?"}
	before := append([]string(nil), patterns...)

	name := "中文a"
	for i := 0; i < 5; i++ {
		got := MatchAny(patterns, name)
		if i == 0 && !got {
			t.Fatalf("MatchAny(%q, %q) should be true", patterns, name)
		}
		if i > 0 && got != true {
			t.Fatalf("MatchAny not stable at call %d: got %v", i, got)
		}
	}
	for i, p := range patterns {
		if p != before[i] {
			t.Fatalf("MatchAny mutated patterns[%d]: %q -> %q", i, before[i], p)
		}
	}

	// 重复匹配同一模式/名字对，结果必须一致。
	for i := 0; i < 5; i++ {
		if got := MatchGlob("a*", "abcdef"); !got {
			t.Fatalf("MatchGlob not stable at call %d: got %v", i, got)
		}
	}
}
