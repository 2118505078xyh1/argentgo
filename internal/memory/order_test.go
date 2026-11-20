package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOrderInstructions(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	parser := filepath.Join(src, "parser")
	lexer := filepath.Join(src, "lexer")
	aaa := filepath.Join(src, "aaa")
	zzz := filepath.Join(src, "zzz")
	cjk := filepath.Join(base, "项目")
	cjkInner := filepath.Join(cjk, "内部")

	inst := func(dir, text string) Instruction {
		return Instruction{Source: filepath.Join(dir, "AGENTS.md"), Text: text}
	}

	tests := []struct {
		name string
		in   []Instruction
		want []string
	}{
		{
			name: "parent before child; nearest last",
			in:   []Instruction{inst(parser, "parser"), inst(base, "root"), inst(src, "src")},
			want: []string{"root", "src", "parser"},
		},
		{
			name: "same depth sorted by path, input order ignored",
			in:   []Instruction{inst(zzz, "zzz"), inst(aaa, "aaa")},
			want: []string{"aaa", "zzz"},
		},
		{
			name: "CJK parent before CJK child",
			in:   []Instruction{inst(cjkInner, "child-cjk"), inst(cjk, "root-cjk")},
			want: []string{"root-cjk", "child-cjk"},
		},
		{
			name: "dedupe by source keeps first occurrence",
			in: []Instruction{
				inst(src, "first"),
				inst(src, "second"),
			},
			want: []string{"first"},
		},
		{
			name: "dedupe after path cleaning",
			in: []Instruction{
				{Source: filepath.Join(src, ".", "AGENTS.md"), Text: "clean"},
				inst(src, "dirty"),
			},
			want: []string{"clean"},
		},
		{
			name: "mixed tree fully ordered and deduped",
			in: []Instruction{
				inst(lexer, "lexer"),
				inst(parser, "parser"),
				inst(base, "root"),
				inst(src, "src"),
				inst(parser, "parser-dup"),
			},
			want: []string{"root", "src", "lexer", "parser"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := textsOf(OrderInstructions(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("OrderInstructions = %#v, want %#v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("OrderInstructions = %#v, want %#v", got, tc.want)
				}
			}
		})
	}
}

func TestOrderInstructionsIsPure(t *testing.T) {
	base := t.TempDir()
	in := []Instruction{
		{Source: filepath.Join(base, "b", "AGENTS.md"), Text: "B"},
		{Source: filepath.Join(base, "a", "AGENTS.md"), Text: "A"},
	}
	snapshot := append([]Instruction(nil), in...)

	out := OrderInstructions(in)
	if len(out) != 2 || out[0].Text != "A" || out[1].Text != "B" {
		t.Fatalf("OrderInstructions = %#v, want [A B]", out)
	}
	// The input slice must be untouched.
	for i := range in {
		if in[i] != snapshot[i] {
			t.Fatalf("input mutated at %d: got %#v, want %#v", i, in[i], snapshot[i])
		}
	}
	// The result must be a fresh slice (no aliasing back to the input).
	out[0].Text = "CHANGED"
	if in[0].Text != "B" || in[1].Text != "A" {
		t.Fatalf("output aliases input: %#v", in)
	}
}

func TestOrderInstructionsEmptyAndSkipsEmptySource(t *testing.T) {
	if got := OrderInstructions(nil); got != nil {
		t.Fatalf("nil docs = %#v, want nil", got)
	}
	if got := OrderInstructions([]Instruction{}); got != nil {
		t.Fatalf("empty docs = %#v, want nil", got)
	}

	base := t.TempDir()
	in := []Instruction{
		{Source: "", Text: "no source"},
		{Source: "   ", Text: "blank source"},
		{Source: filepath.Join(base, "src", "AGENTS.md"), Text: "keep"},
	}
	got := textsOf(OrderInstructions(in))
	if len(got) != 1 || got[0] != "keep" {
		t.Fatalf("OrderInstructions = %#v, want [keep]", got)
	}
	// All sources empty: result is nil, not an empty non-nil slice.
	if got := OrderInstructions(in[:2]); got != nil {
		t.Fatalf("all empty sources = %#v, want nil", got)
	}
}

// TestOrderInstructionsCaseInsensitiveDedupOnWindows guards the documented
// case-insensitive deduplication on Windows; it is a no-op elsewhere.
func TestOrderInstructionsCaseInsensitiveDedupOnWindows(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("case-insensitive path dedup applies on Windows only")
	}
	base := t.TempDir()
	lower := filepath.Join(base, "src", "AGENTS.md")
	upper := filepath.Join(strings.ToUpper(base), "SRC", "AGENTS.MD")
	in := []Instruction{{Source: lower, Text: "lower"}, {Source: upper, Text: "upper"}}
	got := textsOf(OrderInstructions(in))
	if len(got) != 1 || got[0] != "lower" {
		t.Fatalf("OrderInstructions = %#v, want [lower]", got)
	}
}
