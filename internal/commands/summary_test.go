package commands

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatCommandSummaryLayout(t *testing.T) {
	tests := []struct {
		name  string
		cmd   Command
		width int
		want  string
	}{
		{
			name:  "name and description share the first line",
			cmd:   Command{Name: "review", Description: "Review the code"},
			width: 60,
			want:  "review  Review the code",
		},
		{
			name:  "argument hint adds a second line",
			cmd:   Command{Name: "deploy", Description: "Deploy the app", ArgumentHint: "<env>"},
			width: 60,
			want:  "deploy  Deploy the app\n  <env>",
		},
		{
			name:  "name only",
			cmd:   Command{Name: "ship"},
			width: 20,
			want:  "ship",
		},
		{
			name:  "description only",
			cmd:   Command{Description: "Do the thing"},
			width: 20,
			want:  "Do the thing",
		},
		{
			name:  "hint only stays on one line",
			cmd:   Command{ArgumentHint: "<file>"},
			width: 20,
			want:  "<file>",
		},
		{
			name:  "name and hint without description",
			cmd:   Command{Name: "deploy", ArgumentHint: "<env>"},
			width: 60,
			want:  "deploy\n  <env>",
		},
		{
			name:  "description and hint without name",
			cmd:   Command{Description: "Deploy the app", ArgumentHint: "<env>"},
			width: 60,
			want:  "Deploy the app\n  <env>",
		},
		{
			name:  "all fields empty",
			cmd:   Command{},
			width: 60,
			want:  "",
		},
		{
			name:  "namespace prefix is preserved",
			cmd:   Command{Name: "acme:review", Description: "x"},
			width: 60,
			want:  "acme:review  x",
		},
		{
			name:  "embedded whitespace collapses to single spaces",
			cmd:   Command{Name: "  review \n", Description: "Review\t the   code"},
			width: 60,
			want:  "review  Review the code",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatCommandSummary(tt.cmd, tt.width); got != tt.want {
				t.Errorf("FormatCommandSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCommandSummaryTruncation(t *testing.T) {
	tests := []struct {
		name  string
		cmd   Command
		width int
		want  string
	}{
		{
			name:  "long description truncated to width",
			cmd:   Command{Name: "review", Description: strings.Repeat("a", 100)},
			width: 20,
			want:  "review  " + strings.Repeat("a", 12),
		},
		{
			name:  "both lines truncated independently",
			cmd:   Command{Name: "deploy", Description: strings.Repeat("d", 50), ArgumentHint: strings.Repeat("h", 50)},
			width: 10,
			want:  "deploy  dd\n  " + strings.Repeat("h", 8),
		},
		{
			name:  "exact fit is not truncated",
			cmd:   Command{Name: "review", Description: "Review code"},
			width: 19,
			want:  "review  Review code",
		},
		{
			name:  "one cell below exact fit drops the last rune",
			cmd:   Command{Name: "review", Description: "Review code"},
			width: 18,
			want:  "review  Review cod",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatCommandSummary(tt.cmd, tt.width); got != tt.want {
				t.Errorf("FormatCommandSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCommandSummaryCJK(t *testing.T) {
	tests := []struct {
		name  string
		cmd   Command
		width int
		want  string
	}{
		{
			name:  "CJK head truncated at a rune boundary",
			cmd:   Command{Name: "审查", Description: "代码审查"},
			width: 10,
			want:  "审查  代码",
		},
		{
			name:  "wide rune dropped when it would overflow",
			cmd:   Command{Name: "审", Description: "x"},
			width: 2,
			want:  "审",
		},
		{
			name:  "wide rune skipped when only one cell remains",
			cmd:   Command{Name: "审查", Description: "x"},
			width: 1,
			want:  "",
		},
		{
			name:  "fullwidth ASCII counts as two cells",
			cmd:   Command{Name: "ＡＢ", Description: "x"},
			width: 4,
			want:  "ＡＢ",
		},
		{
			name:  "kana counts as two cells",
			cmd:   Command{Name: "レビュー"},
			width: 6,
			want:  "レビュ",
		},
		{
			name:  "emoji counts as two cells",
			cmd:   Command{Name: "deploy", Description: "ship 🚀 now"},
			width: 15,
			want:  "deploy  ship 🚀",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatCommandSummary(tt.cmd, tt.width)
			if got != tt.want {
				t.Errorf("FormatCommandSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCommandSummaryWidthBounds(t *testing.T) {
	tests := []struct {
		name  string
		cmd   Command
		width int
		want  string
	}{
		{name: "zero width", cmd: Command{Name: "review", Description: "x"}, width: 0, want: ""},
		{name: "negative width", cmd: Command{Name: "review", Description: "x"}, width: -5, want: ""},
		{name: "single cell keeps one ASCII rune", cmd: Command{Name: "review", Description: "x"}, width: 1, want: "r"},
		{name: "single cell cannot hold a CJK rune", cmd: Command{Name: "审查"}, width: 1, want: ""},
		{name: "empty command ignores any width", cmd: Command{}, width: 80, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatCommandSummary(tt.cmd, tt.width); got != tt.want {
				t.Errorf("FormatCommandSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCommandSummaryIsRuneSafeAndBounded(t *testing.T) {
	cmd := Command{
		Name:         "审查",
		Description:  strings.Repeat("代码审查", 8),
		ArgumentHint: strings.Repeat("参数", 8),
	}
	const width = 12
	got := FormatCommandSummary(cmd, width)

	if !utf8.ValidString(got) {
		t.Fatalf("summary is not valid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("summary contains a replacement rune: %q", got)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two lines, got %d: %q", len(lines), got)
	}
	for i, line := range lines {
		if w := stringDisplayWidth(line); w > width {
			t.Errorf("line %d display width = %d, want <= %d: %q", i, w, width, line)
		}
	}
}
