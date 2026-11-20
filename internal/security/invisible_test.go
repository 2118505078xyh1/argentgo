package security

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestContainsInvisibleTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"plain ascii", "echo hello", false},
		{"cjk and emoji", "日本語 🚀", false},
		{"bidi controls are not tags", "\u202e\u200f\ufeff", false},
		{"below tag block", "\U000dffff", false},
		{"above tag block", "\U000e0080", false},
		{"lowest tag", "\U000e0000", true},
		{"highest tag", "\U000e007f", true},
		{"tag letter", "echo \U000e0041hi", true},
		{"tag inside other text", "a\U000e0062c", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContainsInvisibleTags(tc.in); got != tc.want {
				t.Fatalf("ContainsInvisibleTags(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscapeBidiEscapesEveryControlRange(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"arabic letter mark", "a\u061Cb", `a\u061cb`},
		{"left to right mark", "a\u200Eb", `a\u200eb`},
		{"right to left mark", "a\u200Fb", `a\u200fb`},
		{"embedding and override", "\u202A\u202B\u202C\u202D\u202E", `\u202a\u202b\u202c\u202d\u202e`},
		{"isolates", "\u2066\u2067\u2068\u2069", `\u2066\u2067\u2068\u2069`},
		{"bom", "a\ufeffb", `a\ufeffb`},
		{"unrelated format runes stay", "\u200B\u2060\u00AD", "\u200B\u2060\u00AD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EscapeBidi(tc.in)
			if got != tc.want {
				t.Fatalf("EscapeBidi(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Check every code point of the two contiguous ranges individually so an
	// off-by-one in either bound is caught.
	for r := rune(0x202a); r <= 0x202e; r++ {
		in := string(r)
		if got, want := EscapeBidi(in), `\u`+hex4(r); got != want {
			t.Fatalf("EscapeBidi(U+%04X) = %q, want %q", r, got, want)
		}
	}
	for r := rune(0x2066); r <= 0x2069; r++ {
		in := string(r)
		if got, want := EscapeBidi(in), `\u`+hex4(r); got != want {
			t.Fatalf("EscapeBidi(U+%04X) = %q, want %q", r, got, want)
		}
	}
}

// hex4 mirrors the lowercase four-digit form used by the production escapes.
func hex4(r rune) string {
	const digits = "0123456789abcdef"
	var out [4]byte
	for i, shift := range []uint{12, 8, 4, 0} {
		out[i] = digits[(r>>shift)&0xf]
	}
	return string(out[:])
}

func TestEscapeBidiKeepsReadableRunesAndInput(t *testing.T) {
	in := "ls -la\n\techo 日本語 🚀 Café;"
	if got := EscapeBidi(in); got != in {
		t.Fatalf("readable text changed: got %q, want %q", got, in)
	}

	raw := "a\u202eb"
	got := EscapeBidi(raw)
	if !strings.Contains(got, `\u202e`) || strings.Contains(got, "\u202e") {
		t.Fatalf("bidi override was not made visible: %q", got)
	}
	if raw != "a\u202eb" {
		t.Fatalf("EscapeBidi mutated its input: %q", raw)
	}
}

func TestSanitizeInvisibleStripsTagsAndEscapesBidi(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		want       string
		wantHadTag bool
	}{
		{"empty", "", "", false},
		{"clean text", "echo hi", "echo hi", false},
		{"bidi only", "a\u200Eb", `a\u200eb`, false},
		{"tags only", "\U000e0041\U000e007f", "", true},
		{"mixed", "echo \U000e0041\u202E日本🚀", `echo \u202e日本🚀`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, hadTags := SanitizeInvisible(tc.in)
			if got != tc.want || hadTags != tc.wantHadTag {
				t.Fatalf("SanitizeInvisible(%q) = (%q, %v), want (%q, %v)",
					tc.in, got, hadTags, tc.want, tc.wantHadTag)
			}
		})
	}
}

func TestPromptApproverWarnsAboutInvisibleTags(t *testing.T) {
	const warning = "⚠ 请求包含不可见字符（已清除 Unicode 标签）"
	var out bytes.Buffer
	approver := NewPromptApprover(strings.NewReader("n\n"), &out)
	if _, err := approver.Ask(context.Background(), "bash", "echo \U000e0041hi"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	prompt := out.String()
	if !strings.HasPrefix(prompt, warning+"\n") {
		t.Fatalf("warning line missing or not first: %q", prompt)
	}
	if strings.Contains(prompt, "\U000e0041") {
		t.Fatalf("hidden tag survived into the prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "echo hi") {
		t.Fatalf("visible text was lost: %q", prompt)
	}
}

func TestPromptApproverEscapesBidiInPrompt(t *testing.T) {
	var out bytes.Buffer
	approver := NewPromptApprover(strings.NewReader("n\n"), &out)
	if _, err := approver.Ask(context.Background(), "bash", "echo \u202Ewhoami"); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	prompt := out.String()
	if !strings.Contains(prompt, `\u202e`) || strings.Contains(prompt, "\u202E") {
		t.Fatalf("bidi override was not made visible in the prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "echo ") || !strings.Contains(prompt, "whoami") {
		t.Fatalf("visible text was lost: %q", prompt)
	}
}

func TestPromptApproverStillApprovesSanitizedArgs(t *testing.T) {
	var out bytes.Buffer
	approver := NewPromptApprover(strings.NewReader("y\n"), &out)
	ok, err := approver.Ask(context.Background(), "bash", "\U000e0041echo ok")
	if err != nil || !ok {
		t.Fatalf("approval semantics changed: ok=%v err=%v", ok, err)
	}
}
