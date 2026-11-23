package tools

import (
	"regexp"
	"strings"
	"testing"
)

func TestCompileSearchPatternFallbackChain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pattern   string
		insens    bool
		wantLevel regexFallback
		wantMatch []string
		wantMiss  []string
		wantHint  string
	}{
		{
			name: "plain regex compiles exactly", pattern: "foo[0-9]+",
			wantLevel: regexExact, wantMatch: []string{"foo123"}, wantMiss: []string{"bar1"},
		},
		{
			name: "dollar brace sanitized", pattern: "${HOME}/bin",
			wantLevel: regexBracesFixed, wantMatch: []string{"prefix ${HOME}/bin suffix"}, wantMiss: []string{"/home/user/bin"},
		},
		{
			name: "valid repetition preserved", pattern: "a{2,4}",
			wantLevel: regexExact, wantMatch: []string{"aa", "aaa", "aaaa"},
			wantMiss: []string{"a", "bbb"},
		},
		{
			name: "unicode class preserved", pattern: `\p{Greek}+`,
			wantLevel: regexExact, wantMatch: []string{"αβγ"}, wantMiss: []string{"abc"},
		},
		{
			name: "isolated open paren escaped", pattern: "fetchProvider(",
			wantLevel: regexParensFixed, wantMatch: []string{"call fetchProvider("}, wantMiss: []string{"call fetchProviderx"},
		},
		{
			name: "isolated close paren escaped", pattern: "abc)",
			wantLevel: regexParensFixed, wantMatch: []string{"an abc) here"},
		},
		{
			name: "lookaround becomes literal with hint", pattern: "(?=x)",
			wantLevel: regexLookaroundLiteral, wantMatch: []string{"prefix (?=x) suffix"},
			wantHint: "RE2",
		},
		{
			name: "negative lookahead becomes literal", pattern: "foo(?!bar)",
			wantLevel: regexLookaroundLiteral, wantMatch: []string{"see foo(?!bar) now"},
			wantHint: "RE2",
		},
		{
			name: "quote meta fallback", pattern: "[",
			wantLevel: regexLiteralFallback, wantMatch: []string{"a [ bracket"}, wantMiss: []string{"no bracket"},
			wantHint: "字面量",
		},
		{
			name: "case insensitive prefix kept", pattern: "hello", insens: true,
			wantLevel: regexExact, wantMatch: []string{"HELLO world"}, wantMiss: []string{"bye"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			re, _, level, hint := compileSearchPattern(tc.pattern, tc.insens)
			if level != tc.wantLevel {
				t.Fatalf("降级层级 = %v，期望 %v（提示 %q）", level, tc.wantLevel, hint)
			}
			if tc.wantHint != "" && !strings.Contains(hint, tc.wantHint) {
				t.Fatalf("提示 %q 不含 %q", hint, tc.wantHint)
			}
			for _, s := range tc.wantMatch {
				if !re.MatchString(s) {
					t.Fatalf("模式 %q 应命中 %q，实际未命中（有效模式见上）", tc.pattern, s)
				}
			}
			for _, s := range tc.wantMiss {
				if re.MatchString(s) {
					t.Fatalf("模式 %q 不应命中 %q", tc.pattern, s)
				}
			}
		})
	}
}

func TestCompileSearchPatternProducesValidRegex(t *testing.T) {
	// 任意畸形输入都必须产出可编译的正则，绝不 panic。
	for _, p := range []string{"", "(", ")", "[", "*", "**", `(?P<`, `\`, `a{`, `${`, `(?<=x)`} {
		re, _, _, _ := compileSearchPattern(p, false)
		if re == nil {
			t.Fatalf("模式 %q 返回了 nil 正则", p)
		}
		if _, err := regexp.Compile(re.String()); err != nil {
			t.Fatalf("模式 %q 的产物 %q 无法编译：%v", p, re.String(), err)
		}
	}
}

func TestRegexHintSuffixNonEmptyOnlyOnFallback(t *testing.T) {
	if got := regexHintSuffix(regexExact); got != "" {
		t.Fatalf("精确匹配不应有尾注：%q", got)
	}
	if got := regexHintSuffix(regexLookaroundLiteral); !strings.Contains(got, "RE2") {
		t.Fatalf("lookaround 尾注 = %q", got)
	}
}
