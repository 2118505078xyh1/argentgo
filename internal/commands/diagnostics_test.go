package commands

import (
	"reflect"
	"strings"
	"testing"
)

func TestDiagnoseCommandsTable(t *testing.T) {
	cases := []struct {
		name string
		cmds []Command
		want []DiagnosticKind
	}{
		{
			name: "empty input",
			cmds: nil,
			want: nil,
		},
		{
			name: "one valid command",
			cmds: []Command{
				{Name: "review", Body: "Review $ARGUMENTS", Path: "p/review.md"},
			},
			want: nil,
		},
		{
			name: "duplicate name shadows with first as winner",
			cmds: []Command{
				{Name: "review", Body: "body", Path: "project/review.md"},
				{Name: "review", Body: "body", Path: "home/review.md"},
			},
			want: []DiagnosticKind{DiagnosticDuplicateName},
		},
		{
			name: "CJK names and namespaces are valid and distinct",
			cmds: []Command{
				{Name: "审查", Body: "body", Path: "p/审查.md"},
				{Name: "审查二", Body: "body", Path: "p/审查二.md"},
				{Name: "review", Body: "body", Path: "p/review.md"},
				{Name: "acme:review", Body: "body", Path: "plug/review.md", Plugin: "acme"},
			},
			want: nil,
		},
		{
			name: "empty and malformed names",
			cmds: []Command{
				{Name: "", Body: "body", Path: "p/empty.md"},
				{Name: "a::b", Body: "body", Path: "p/a.md"},
				{Name: ":review", Body: "body", Path: "p/b.md"},
				{Name: "review:", Body: "body", Path: "p/c.md"},
				{Name: "has space", Body: "body", Path: "p/d.md"},
			},
			want: []DiagnosticKind{
				DiagnosticInvalidName, DiagnosticInvalidName, DiagnosticInvalidName,
				DiagnosticInvalidName, DiagnosticInvalidName,
			},
		},
		{
			name: "whitespace-only difference is still a duplicate name",
			cmds: []Command{
				{Name: "review", Body: "body", Path: "p/a.md"},
				{Name: " review ", Body: "body", Path: "p/b.md"},
			},
			want: []DiagnosticKind{DiagnosticInvalidName, DiagnosticDuplicateName},
		},
		{
			name: "missing argument hint uses highest nested position",
			cmds: []Command{
				{Name: "x", Body: "first $1 second ${12}", Path: "p/x.md"},
			},
			want: []DiagnosticKind{DiagnosticMissingArgument},
		},
		{
			name: "nested braced placeholders are real positions",
			cmds: []Command{
				{Name: "x", Body: "$ARGUMENTS and ${${1}} and $@", Path: "p/x.md"},
			},
			want: []DiagnosticKind{DiagnosticMissingArgument},
		},
		{
			name: "argument hint present suppresses missing-argument",
			cmds: []Command{
				{Name: "x", Body: "first $1 second $2", ArgumentHint: "<a> <b>", Path: "p/x.md"},
			},
			want: nil,
		},
		{
			name: "empty body",
			cmds: []Command{
				{Name: "x", Body: "  \n ", Path: "p/x.md"},
			},
			want: []DiagnosticKind{DiagnosticEmptyBody},
		},
		{
			name: "duplicate source path",
			cmds: []Command{
				{Name: "a", Body: "body", Path: "same/dir/a.md"},
				{Name: "b", Body: "body", Path: "same/dir/a.md"},
			},
			want: []DiagnosticKind{DiagnosticDuplicateSource},
		},
		{
			name: "invisible content becomes a diagnostic",
			cmds: []Command{
				{Name: "x", Body: "body", Path: "p/x.md", Warnings: []string{"frontmatter contains zero-width space"}},
			},
			want: []DiagnosticKind{DiagnosticInvalidContent},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]Command(nil), tc.cmds...)

			got := DiagnoseCommands(tc.cmds)
			var kinds []DiagnosticKind
			for _, d := range got {
				kinds = append(kinds, d.Kind)
			}
			if !reflect.DeepEqual(kinds, tc.want) {
				t.Fatalf("kinds = %v, want %v (diagnostics: %+v)", kinds, tc.want, got)
			}
			if !reflect.DeepEqual(tc.cmds, before) {
				t.Error("DiagnoseCommands mutated its input")
			}
		})
	}
}

func TestDiagnoseCommandsDuplicateNameReportsWinner(t *testing.T) {
	cmds := []Command{
		{Name: "review", Body: "body", Path: "project/review.md"},
		{Name: "review", Body: "body", Path: "home/review.md"},
		{Name: "review", Body: "body", Path: "plugin/review.md"},
	}
	got := DiagnoseCommands(cmds)
	if len(got) != 2 {
		t.Fatalf("diagnostics = %+v, want 2 shadowed entries", got)
	}
	for i, wantLoser := range []string{"home/review.md", "plugin/review.md"} {
		d := got[i]
		if d.Kind != DiagnosticDuplicateName || d.Name != "review" {
			t.Errorf("[%d] = %+v", i, d)
		}
		if d.Path != wantLoser {
			t.Errorf("[%d] path = %q, want %q", i, d.Path, wantLoser)
		}
		if d.Winner != "project/review.md" {
			t.Errorf("[%d] winner = %q", i, d.Winner)
		}
	}
}

func TestDiagnoseCommandsMissingArgumentMessage(t *testing.T) {
	got := DiagnoseCommands([]Command{{Name: "x", Body: "$1 ${20}", Path: "p/x.md"}})
	if len(got) != 1 || got[0].Kind != DiagnosticMissingArgument {
		t.Fatalf("diagnostics = %+v", got)
	}
	if !strings.Contains(got[0].Message, "$20") {
		t.Errorf("message = %q, want the highest position", got[0].Message)
	}
}

func TestDiagnoseCommandTextReportsInvalidFrontmatterLine(t *testing.T) {
	text := "---\ndescription: fine\nthis is not a field\n---\nbody"
	got := DiagnoseCommandText(text, "broken.md")
	if len(got) != 1 || got[0].Kind != DiagnosticInvalidFrontmatter {
		t.Fatalf("diagnostics = %+v", got)
	}
	if got[0].Line != 3 || got[0].Path != "broken.md" {
		t.Errorf("diagnostic = %+v, want broken.md line 3", got[0])
	}
}

func TestDiagnoseCommandTextParsesCJKName(t *testing.T) {
	text := "---\nname: 审查\ndescription: CJK command\nargument-hint: <主题>\n---\n审查 $ARGUMENTS"
	if got := DiagnoseCommandText(text, "审查.md"); len(got) != 0 {
		t.Fatalf("diagnostics = %+v, want none", got)
	}
}
