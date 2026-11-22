package agent

import (
	"strings"
	"testing"
)

func TestMagicKeywordDirective(t *testing.T) {
	ultra := DefaultMagicKeywords[0].Directive
	chill := DefaultMagicKeywords[1].Directive
	both := ultra + "\n" + chill

	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{name: "plain prose", prompt: "Please ultrathink about the failure modes before changing this API.", want: ultra},
		{name: "case insensitive", prompt: "ULTRATHINK this and then ChIlLaX for a quick reply", want: both},
		{name: "word boundary rejects derived word", prompt: "ultrathinking is not a trigger, but ultrathink is", want: ultra},
		{name: "word boundary rejects embedded word", prompt: "myultrathink and ultrathinks are not triggers", want: ""},
		{name: "fenced code with backticks", prompt: "before\n```go\nultrathink()\nchillax\n```\nafter", want: ""},
		{name: "fenced code with tildes", prompt: "~~~\nultrathink\n~~~", want: ""},
		{name: "inline code", prompt: "Use `ultrathink` and `chillax` in code, not prose.", want: ""},
		{name: "path token", prompt: "Read docs/ultrathink.md and ultrathink.go and C:\\chillax", want: ""},
		{name: "multiple keywords follow default order", prompt: "chillax first, then ultrathink please", want: both},
		{name: "duplicates deduped", prompt: "ultrathink ultrathink ULTRATHINK", want: ultra},
		{name: "no match", prompt: "just a normal prompt with no triggers", want: ""},
		{name: "empty prompt", prompt: "", want: ""},
		{name: "cjk adjacent with separators", prompt: "请 ultrathink 分析，然后 chillax 一下。", want: both},
		{name: "cjk adjacent without separator is rune safe", prompt: "请ultrathink一下", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MagicKeywordDirective(tt.prompt); got != tt.want {
				t.Errorf("MagicKeywordDirective(%q) = %q, want %q", tt.prompt, got, tt.want)
			}
		})
	}
}

func TestMagicKeywordDirectiveIdempotent(t *testing.T) {
	const prompt = "please chillax and ultrathink on this"
	first := MagicKeywordDirective(prompt)
	second := MagicKeywordDirective(prompt)
	if first == "" {
		t.Fatal("expected directives from prose keywords")
	}
	if first != second {
		t.Fatalf("repeated calls differ: %q vs %q", first, second)
	}
	// 注入的指令文本本身不含触发词，再次扫描不应产生新指令。
	if again := MagicKeywordDirective(first); again != "" {
		t.Errorf("feeding directives back produced new directives: %q", again)
	}
	if prompt != "please chillax and ultrathink on this" {
		t.Errorf("input was mutated: %q", prompt)
	}
}

func TestMagicKeywordDirectiveOrderMatchesDefaults(t *testing.T) {
	// 关键词逆序出现，输出仍须按 DefaultMagicKeywords 的顺序拼接。
	reversed := make([]string, 0, len(DefaultMagicKeywords))
	for i := len(DefaultMagicKeywords) - 1; i >= 0; i-- {
		reversed = append(reversed, DefaultMagicKeywords[i].Word)
	}
	want := make([]string, 0, len(DefaultMagicKeywords))
	for _, kw := range DefaultMagicKeywords {
		want = append(want, kw.Directive)
	}
	if got := MagicKeywordDirective(strings.Join(reversed, ", ")); got != strings.Join(want, "\n") {
		t.Errorf("order = %q, want %q", got, strings.Join(want, "\n"))
	}
}

func TestMagicKeywordDirectiveIgnoresMutableRegistry(t *testing.T) {
	// 纯函数契约：扫描不读取可变的 DefaultMagicKeywords，调用方的改动不影响结果。
	saved := append([]MagicKeyword(nil), DefaultMagicKeywords...)
	t.Cleanup(func() { DefaultMagicKeywords = saved })

	DefaultMagicKeywords = nil
	if got := MagicKeywordDirective("please ultrathink"); got != saved[0].Directive {
		t.Errorf("result depended on mutable registry: %q", got)
	}
}
