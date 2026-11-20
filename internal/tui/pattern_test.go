package tui

import (
	"reflect"
	"testing"
)

// subseqFuzzy is a minimal stand-in for the real matcher: pattern and text
// arrive already case-normalised from matchOne.
func subseqFuzzy(pattern, text string) (int, []int, bool) {
	pr, tr := []rune(pattern), []rune(text)
	positions := make([]int, 0, len(pr))
	j := 0
	for i, r := range tr {
		if j < len(pr) && r == pr[j] {
			positions = append(positions, i)
			j++
		}
	}
	if j == len(pr) {
		return len(pr), positions, true
	}
	return 0, nil, false
}

func TestParsePatternSyntax(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []SearchTerm
	}{
		{"plain term is fuzzy", "abc", []SearchTerm{{Kind: TermFuzzy, Text: "abc"}}},
		{"leading quote is exact substring", "'abc", []SearchTerm{{Kind: TermExact, Text: "abc"}}},
		{"caret is prefix", "^abc", []SearchTerm{{Kind: TermPrefix, Text: "abc"}}},
		{"dollar is suffix", "abc$", []SearchTerm{{Kind: TermSuffix, Text: "abc"}}},
		{"quoted both ends is bounded exact", "'abc'", []SearchTerm{{Kind: TermEqual, Text: "abc"}}},
		{"bang is inverse", "!abc", []SearchTerm{{Kind: TermInverse, Text: "abc"}}},
		{"space separates AND terms", "foo bar", []SearchTerm{
			{Kind: TermFuzzy, Text: "foo"}, {Kind: TermFuzzy, Text: "bar"},
		}},
		{"pipe separates OR groups", "a|b", []SearchTerm{
			{Kind: TermFuzzy, Text: "a"}, {Kind: TermOr}, {Kind: TermFuzzy, Text: "b"},
		}},
		{"spaced pipe separates OR groups", "a | b", []SearchTerm{
			{Kind: TermFuzzy, Text: "a"}, {Kind: TermOr}, {Kind: TermFuzzy, Text: "b"},
		}},
		{"combined prefix and inverse", "^src/ !test", []SearchTerm{
			{Kind: TermPrefix, Text: "src/"}, {Kind: TermInverse, Text: "test"},
		}},
		{"mixed operators and OR", "^src/ !test | 'fixture$", []SearchTerm{
			{Kind: TermPrefix, Text: "src/"}, {Kind: TermInverse, Text: "test"},
			{Kind: TermOr},
			{Kind: TermExact, Text: "fixture$"},
		}},
		{"tabs separate AND terms", "foo\tbar", []SearchTerm{
			{Kind: TermFuzzy, Text: "foo"}, {Kind: TermFuzzy, Text: "bar"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePattern(tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParsePattern(%q) = %+v, want %+v", tt.query, got, tt.want)
			}
		})
	}
}

func TestParsePatternEmptyQuery(t *testing.T) {
	for _, query := range []string{"", "   ", "\t\n"} {
		if got := ParsePattern(query); len(got) != 0 {
			t.Errorf("ParsePattern(%q) = %+v, want empty", query, got)
		}
	}
}

func TestParsePatternSmartCase(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"abc", false},
		{"Abc", true},
		{"aB", true},
		{"^abc", false},
		{"^ABC", true},
		{"abc$", false},
		{"'Abc", true},
		{"!ABC", true},
		{"中文", false},
		{"中文ABC", true},
	}
	for _, tt := range tests {
		terms := ParsePattern(tt.query)
		if len(terms) != 1 {
			t.Fatalf("ParsePattern(%q) returned %d terms, want 1", tt.query, len(terms))
		}
		if got := terms[0].CaseSensitive; got != tt.want {
			t.Errorf("ParsePattern(%q) CaseSensitive = %v, want %v", tt.query, got, tt.want)
		}
	}
}

func TestParsePatternEscaping(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  SearchTerm
	}{
		{"escaped caret is literal", `\^abc`, SearchTerm{Kind: TermFuzzy, Text: "^abc"}},
		{"escaped quote is literal", `\'abc`, SearchTerm{Kind: TermFuzzy, Text: "'abc"}},
		{"escaped bang is literal", `\!abc`, SearchTerm{Kind: TermFuzzy, Text: "!abc"}},
		{"escaped dollar is literal", `abc\$`, SearchTerm{Kind: TermFuzzy, Text: "abc$"}},
		{"escaped pipe stays in term", `a\|b`, SearchTerm{Kind: TermFuzzy, Text: "a|b"}},
		{"escaped space stays in term", `a\ b`, SearchTerm{Kind: TermFuzzy, Text: "a b"}},
		{"escaped backslash", `\\`, SearchTerm{Kind: TermFuzzy, Text: `\`}},
		{"closing quote may be escaped", `'abc\'`, SearchTerm{Kind: TermExact, Text: "abc'"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePattern(tt.query)
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("ParsePattern(%q) = %+v, want [%+v]", tt.query, got, tt.want)
			}
		})
	}
}

func TestParsePatternUTF8(t *testing.T) {
	tests := []struct {
		query string
		want  []SearchTerm
	}{
		{"中文", []SearchTerm{{Kind: TermFuzzy, Text: "中文"}}},
		{"'中文", []SearchTerm{{Kind: TermExact, Text: "中文"}}},
		{"^中文", []SearchTerm{{Kind: TermPrefix, Text: "中文"}}},
		{"中文$", []SearchTerm{{Kind: TermSuffix, Text: "中文"}}},
		{"'中文'", []SearchTerm{{Kind: TermEqual, Text: "中文"}}},
		{"!中文", []SearchTerm{{Kind: TermInverse, Text: "中文"}}},
	}
	for _, tt := range tests {
		got := ParsePattern(tt.query)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParsePattern(%q) = %+v, want %+v", tt.query, got, tt.want)
		}
	}
}

func TestMatchPatternSyntax(t *testing.T) {
	tests := []struct {
		name  string
		query string
		text  string
		want  bool
	}{
		{"fuzzy subsequence", "abc", "xxabcxx", true},
		{"fuzzy wrong order", "abc", "acb", false},
		{"exact substring", "'abc", "xxabcxx", true},
		{"exact substring absent", "'abc", "acb", false},
		{"prefix at start", "^abc", "abcdef", true},
		{"prefix in middle", "^abc", "xabc", false},
		{"suffix at end", "abc$", "xxabc", true},
		{"suffix in middle", "abc$", "abcx", false},
		{"bounded exact whole word", "'abc'", "abc", true},
		{"bounded exact inside text", "'abc'", "an abc story", true},
		{"bounded exact glued to letter", "'abc'", "xabcx", false},
		{"bounded exact glued to digit", "'1'", "a1b", false},
		{"inverse excludes match", "!abc", "abcdef", false},
		{"inverse keeps non-match", "!abc", "zzz", true},
		{"OR left", "a | b", "xxaxx", true},
		{"OR right", "a | b", "xxbxx", true},
		{"OR neither", "a | b", "zzz", false},
		{"AND all present", "foo bar", "foo then bar", true},
		{"AND one missing", "foo bar", "foo only", false},
		{"prefix and inverse", "^src/ !test", "src/main.go", true},
		{"prefix and inverse rejects", "^src/ !test", "src/test.go", false},
		{"prefix and inverse wrong dir", "^src/ !test", "lib/main.go", false},
		{"AND binds tighter than OR left", "x a | b y", "x a", true},
		{"AND binds tighter than OR right", "x a | b y", "b y", true},
		{"AND binds tighter than OR rejects", "x a | b y", "x b", false},
		{"lone separator matches all", "|", "anything", true},
		{"trailing OR keeps left group", "a |", "a", true},
		{"trailing OR rejects non-match", "a |", "zzz", false},
		{"leading OR keeps right group", "| a", "a", true},
		{"double OR skips empty group", "a || b", "b", true},
		{"smart case sensitive mismatch", "ABC", "abc", false},
		{"smart case insensitive match", "abc", "ABC", true},
		{"smart case exact mismatch", "'ABC", "xxabcxx", false},
		{"smart case exact match", "'ABC", "xxABCxx", true},
		{"utf8 fuzzy", "中文", "这里中文测试", true},
		{"utf8 exact", "'中文", "hello 中文 world", true},
		{"utf8 prefix", "^中文", "中文abc", true},
		{"utf8 suffix", "中文$", "abc中文", true},
		{"utf8 bounded", "'中文'", "a 中文 b", true},
		{"utf8 bounded rejects glued", "'中文'", "x中文x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			terms := ParsePattern(tt.query)
			if got := MatchPattern(terms, tt.text, subseqFuzzy); got != tt.want {
				t.Errorf("MatchPattern(%q, %q) = %v, want %v", tt.query, tt.text, got, tt.want)
			}
		})
	}
}

func TestMatchPatternEmptyTerms(t *testing.T) {
	for _, query := range []string{"", "   ", "|"} {
		if !MatchPattern(ParsePattern(query), "any text", subseqFuzzy) {
			t.Errorf("empty pattern %q should match every line", query)
		}
	}
}

func TestMatchPatternNilFuzzyFallsBackToSubstring(t *testing.T) {
	terms := ParsePattern("foo bar")
	if !MatchPattern(terms, "bar and foo", nil) {
		t.Error("nil fuzzy should fall back to substring matching")
	}
	if MatchPattern(terms, "foo only", nil) {
		t.Error("nil fuzzy fallback still requires every AND term")
	}
}

func TestMatchPatternFuzzyReceivesNormalisedInput(t *testing.T) {
	var gotPattern, gotText string
	capture := func(p, text string) (int, []int, bool) {
		gotPattern, gotText = p, text
		return 0, nil, true
	}

	MatchPattern(ParsePattern("aBc"), "XYZ", capture)
	if gotPattern != "aBc" || gotText != "XYZ" {
		t.Errorf("smart-case term args = (%q, %q), want (\"aBc\", \"XYZ\")", gotPattern, gotText)
	}

	MatchPattern(ParsePattern("abc"), "XYZ", capture)
	if gotPattern != "abc" || gotText != "xyz" {
		t.Errorf("case-insensitive term args = (%q, %q), want (\"abc\", \"xyz\")", gotPattern, gotText)
	}
}
