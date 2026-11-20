package commands

import (
	"reflect"
	"slices"
	"testing"
)

func TestParseVerifyCommandsTable(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		want       map[string][]string
		wantIssues []IssueKind
	}{
		{
			name: "nil input gives empty non-nil map",
			args: nil,
			want: map[string][]string{},
		},
		{
			name: "single language prefix",
			args: []string{"go: go vet ./..."},
			want: map[string][]string{"go": {"go", "vet", "./..."}},
		},
		{
			name: "multiple language prefixes",
			args: []string{"go: go vet ./...", "python: flake8 --select=E"},
			want: map[string][]string{
				"go":     {"go", "vet", "./..."},
				"python": {"flake8", "--select=E"},
			},
		},
		{
			name: "colon inside command is preserved",
			args: []string{"python: flake8 --config http://example.com/setup.cfg"},
			want: map[string][]string{
				"python": {"flake8", "--config", "http://example.com/setup.cfg"},
			},
		},
		{
			name: "bare command uses all-languages key",
			args: []string{"go vet ./..."},
			want: map[string][]string{VerifyAllLanguages: {"go", "vet", "./..."}},
		},
		{
			name: "mixed prefixed and bare commands",
			args: []string{"go: go vet ./...", "go test ./..."},
			want: map[string][]string{
				"go":               {"go", "vet", "./..."},
				VerifyAllLanguages: {"go", "test", "./..."},
			},
		},
		{
			name: "language aliases and dotted extensions normalize",
			args: []string{
				"golang: go vet ./...",
				".py: flake8",
				"TS: tsc --noEmit",
				"js: eslint .",
			},
			want: map[string][]string{
				"go":         {"go", "vet", "./..."},
				"python":     {"flake8"},
				"typescript": {"tsc", "--noEmit"},
				"javascript": {"eslint", "."},
			},
		},
		{
			name: "unknown language-like prefix is kept as-is",
			args: []string{"pylint: pylint src"},
			want: map[string][]string{"pylint": {"pylint", "src"}},
		},
		{
			name: "windows path after colon is not split",
			args: []string{`C:\tools\vet.exe -x`},
			want: map[string][]string{VerifyAllLanguages: {`C:\tools\vet.exe`, "-x"}},
		},
		{
			name: "bare URL is not split",
			args: []string{"http://example.com/lint"},
			want: map[string][]string{VerifyAllLanguages: {"http://example.com/lint"}},
		},
		{
			name: "colon followed by space but non-token prefix is bare command",
			args: []string{"echo hello: world"},
			want: map[string][]string{VerifyAllLanguages: {"echo", "hello:", "world"}},
		},
		{
			name: "empty command after prefix",
			args: []string{"go:", "python:   "},
			want: map[string][]string{},
			wantIssues: []IssueKind{
				IssueEmptyCommand, IssueEmptyCommand,
			},
		},
		{
			name: "empty bare command",
			args: []string{"", "   "},
			want: map[string][]string{},
			wantIssues: []IssueKind{
				IssueEmptyCommand, IssueEmptyCommand,
			},
		},
		{
			name: "empty language",
			args: []string{": flake8", ":"},
			want: map[string][]string{},
			wantIssues: []IssueKind{
				IssueEmptyLanguage, IssueEmptyLanguage,
			},
		},
		{
			name: "duplicate after normalization keeps last",
			args: []string{"go: go vet ./...", "golang: go build ./..."},
			want: map[string][]string{"go": {"go", "build", "./..."}},
			wantIssues: []IssueKind{
				IssueDuplicateLanguage,
			},
		},
		{
			name: "bad entry does not discard valid ones",
			args: []string{"go: go vet ./...", "go:", "python: flake8"},
			want: map[string][]string{
				"go":     {"go", "vet", "./..."},
				"python": {"flake8"},
			},
			wantIssues: []IssueKind{IssueEmptyCommand},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotIssues := ParseVerifyCommands(tc.args)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseVerifyCommands(%q) map = %#v, want %#v", tc.args, got, tc.want)
			}
			gotKinds := make([]IssueKind, 0, len(gotIssues))
			for _, issue := range gotIssues {
				gotKinds = append(gotKinds, issue.Kind)
			}
			if !slices.Equal(gotKinds, tc.wantIssues) {
				t.Fatalf("ParseVerifyCommands(%q) issue kinds = %v, want %v", tc.args, gotKinds, tc.wantIssues)
			}
		})
	}
}

func TestParseVerifyCommandsIssueMetadata(t *testing.T) {
	args := []string{"go: go vet ./...", "go:", "python: flake8"}
	_, issues := ParseVerifyCommands(args)
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	issue := issues[0]
	if issue.Kind != IssueEmptyCommand {
		t.Errorf("Kind = %q, want %q", issue.Kind, IssueEmptyCommand)
	}
	if issue.Index != 1 {
		t.Errorf("Index = %d, want 1", issue.Index)
	}
	if issue.Arg != "go:" {
		t.Errorf("Arg = %q, want %q", issue.Arg, "go:")
	}
	if issue.Message == "" {
		t.Error("Message is empty")
	}
}

func TestParseVerifyCommandsDoesNotMutateInput(t *testing.T) {
	args := []string{"go: go vet ./...", "python: flake8"}
	original := append([]string(nil), args...)
	_, _ = ParseVerifyCommands(args)
	if !reflect.DeepEqual(args, original) {
		t.Fatalf("ParseVerifyCommands mutated its input: got %q, want %q", args, original)
	}
}
