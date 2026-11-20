package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestContextBarRoundsToNearestCell(t *testing.T) {
	tests := []struct {
		name  string
		pct   int
		cells int
		want  string
	}{
		{"zero", 0, 10, "[░░░░░░░░░░]"},
		{"one rounds down", 1, 10, "[░░░░░░░░░░]"},
		{"five rounds up", 5, 10, "[█░░░░░░░░░]"},
		{"forty nine rounds up", 49, 10, "[█████░░░░░]"},
		{"half", 50, 10, "[█████░░░░░]"},
		{"eighty five", 85, 10, "[█████████░]"},
		{"ninety nine", 99, 10, "[██████████]"},
		{"full", 100, 10, "[██████████]"},
		{"negative clamps to empty", -5, 10, "[░░░░░░░░░░]"},
		{"over hundred clamps to full", 150, 10, "[██████████]"},
		{"zero cells", 50, 0, "[]"},
		{"negative cells", 50, -3, "[]"},
		{"single cell empty", 49, 1, "[░]"},
		{"single cell full", 50, 1, "[█]"},
		{"wide bar", 25, 20, "[█████░░░░░░░░░░░░░░░]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextBar(tc.pct, tc.cells); got != tc.want {
				t.Fatalf("contextBar(%d, %d) = %q, want %q", tc.pct, tc.cells, got, tc.want)
			}
		})
	}
}

func TestContextBarGlyphsAreOneCellEach(t *testing.T) {
	if RuneWidth('█') != 1 || RuneWidth('░') != 1 {
		t.Fatal("the bar glyphs must each occupy exactly one terminal cell")
	}
	for _, pct := range []int{0, 1, 49, 50, 85, 99, 100} {
		bar := contextBar(pct, 10)
		if got := TextWidth(bar); got != 12 { // 10 glyphs + two brackets
			t.Errorf("contextBar(%d, 10) width = %d, want 12 (%q)", pct, got, bar)
		}
		if got := utf8.RuneCountInString(bar); got != 12 {
			t.Errorf("contextBar(%d, 10) rune count = %d, want 12 (%q)", pct, got, bar)
		}
	}
}

func TestContextStyleThresholds(t *testing.T) {
	m := New(Config{})
	tests := []struct {
		pct  int
		want Style
	}{
		{0, m.palette.muted()},
		{49, m.palette.muted()},
		{50, m.palette.warning()},
		{85, m.palette.warning()},
		{86, m.palette.failure()},
		{100, m.palette.failure()},
	}
	for _, tc := range tests {
		if got := m.contextStyle(tc.pct); got != tc.want {
			t.Errorf("contextStyle(%d) = %+v, want %+v", tc.pct, got, tc.want)
		}
	}
}

func TestHeaderContextBarUsesThresholdStyle(t *testing.T) {
	tests := []struct {
		pct  int
		want Style
	}{
		{23, New(Config{}).palette.muted()},
		{60, New(Config{}).palette.warning()},
		{95, New(Config{}).palette.failure()},
	}
	for _, tc := range tests {
		m := New(Config{})
		m.now = func() time.Time { return m.started }
		m.SetUsage(tc.pct*10, 0)
		m.SetContextInfo(1000, "")
		span, ok := contextSpan(m.headerLocked(120))
		if !ok {
			t.Fatalf("no ctx span for %d%%", tc.pct)
		}
		wantText := "ctx " + contextBar(tc.pct, contextBarCells) + " " + strconv.Itoa(tc.pct) + "%"
		if span.Text != wantText {
			t.Errorf("ctx span text = %q, want %q", span.Text, wantText)
		}
		if span.Style != tc.want {
			t.Errorf("ctx span style at %d%% = %+v, want %+v", tc.pct, span.Style, tc.want)
		}
	}
}

func TestHeaderContextBarIsPlainWhenColourSuppressed(t *testing.T) {
	env := func(key, value string) func(string) string {
		return func(k string) string {
			if k == key {
				return value
			}
			return ""
		}
	}
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no_color flag", Config{NoColor: true}},
		{"NO_COLOR env", Config{Env: env("NO_COLOR", "1")}},
		{"dumb terminal", Config{Env: env("TERM", "dumb")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(tc.cfg)
			m.now = func() time.Time { return m.started }
			m.SetUsage(950, 0)
			m.SetContextInfo(1000, "")
			span, ok := contextSpan(m.headerLocked(120))
			if !ok {
				t.Fatal("no ctx span")
			}
			if span.Style != (Style{}) {
				t.Fatalf("ctx span must be unstyled when colour is suppressed, got %+v", span.Style)
			}
			wantText := "ctx " + contextBar(95, contextBarCells) + " 95%"
			if span.Text != wantText {
				t.Fatalf("ctx span text = %q, want %q", span.Text, wantText)
			}
		})
	}
}

// contextSpan finds the header span that carries the context usage readout.
func contextSpan(line Line) (Span, bool) {
	for _, span := range line.Spans {
		if strings.HasPrefix(span.Text, "ctx ") {
			return span, true
		}
	}
	return Span{}, false
}
