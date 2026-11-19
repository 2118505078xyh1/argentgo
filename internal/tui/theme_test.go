package tui

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

var sgrPattern = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestPaletteLookupCoversAllThemes(t *testing.T) {
	for _, name := range ThemeNames() {
		p, err := paletteFor(name)
		if err != nil {
			t.Fatalf("paletteFor(%q): %v", name, err)
		}
		for _, role := range []struct {
			name  string
			color Color
		}{
			{"user", p.User}, {"assistant", p.Assistant}, {"tool", p.Tool},
			{"reasoning", p.Reasoning}, {"error", p.Error}, {"accent", p.Accent},
			{"border", p.Border}, {"muted", p.Muted},
		} {
			if role.color == (Color{}) && role.name != "assistant" {
				t.Errorf("%s: role %q has no colour", name, role.name)
			}
			if hasRGB(role.color) {
				t.Errorf("%s: role %q uses a 256/RGB colour", name, role.name)
			}
		}
	}

	dark, _ := paletteFor(ThemeDark)
	light, _ := paletteFor(ThemeLight)
	if dark.Border == light.Border {
		t.Error("light theme reuses the dark border colour")
	}
	if dark.User != (ColorCyan) {
		t.Errorf("dark user colour = %q, want the historic cyan", dark.User.fg)
	}

	if _, err := paletteFor("neon"); !errors.Is(err, ErrUnknownTheme) {
		t.Errorf("unknown theme error = %v, want ErrUnknownTheme", err)
	}
}

func TestANSIThemeUsesNoRGBEscapes(t *testing.T) {
	for _, name := range ThemeNames() {
		m := New(Config{Theme: name, Locale: "en"})
		m.AppendUser("hello")
		m.AppendTool("bash", `{"command":"go test"}`, "ok", false)
		m.AppendReasoning("thinking")
		m.AppendNotice("note")

		frame := m.Frame(100, 24)
		var out strings.Builder
		if _, err := frame.Flush(&out); err != nil {
			t.Fatalf("flush: %v", err)
		}
		written := out.String()
		for _, marker := range []string{"38;2;", "48;2;", "38;5;", "48;5;"} {
			if strings.Contains(written, marker) {
				t.Errorf("%s theme emitted %q: %q", name, marker, written)
			}
		}
	}
}

func hasRGB(c Color) bool {
	for _, code := range []string{c.fg, c.bg} {
		if strings.HasPrefix(code, "38;2;") || strings.HasPrefix(code, "48;2;") ||
			strings.HasPrefix(code, "38;5;") || strings.HasPrefix(code, "48;5;") {
			return true
		}
	}
	return false
}

func TestNoColorEmitsZeroSGRColourSequences(t *testing.T) {
	m := New(Config{NoColor: true, Locale: "en"})
	m.AppendUser("hello")
	m.AppendTool("bash", `{"command":"go test"}`, "ok", false)
	m.AppendReasoning("weighing options")
	m.SetStatus("busy")
	buffer := NewBuffer(100, 24)
	buffer.SetNoColor(m.NoColor())
	m.Paint(buffer)

	var out strings.Builder
	if _, err := buffer.Flush(&out); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := sgrPattern.FindAllString(out.String(), -1); len(got) != 0 {
		t.Fatalf("NoColor emitted %d SGR sequences, e.g. %q", len(got), got[0])
	}
	if !strings.Contains(out.String(), "hello") {
		t.Error("NoColor frame lost its text")
	}
}

func TestNoColorOptionAndEnvironmentDegradeToMonochrome(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		noColor  bool
		degraded bool
	}{
		{"default", map[string]string{"TERM": "xterm-256color"}, false, false},
		{"NO_COLOR", map[string]string{"NO_COLOR": "1", "TERM": "xterm"}, false, true},
		{"dumb terminal", map[string]string{"TERM": "dumb"}, false, true},
		{"explicit NoColor", map[string]string{"TERM": "xterm"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(key string) string { return tc.env[key] }
			m := New(Config{Locale: "en", Env: lookup, NoColor: tc.noColor})
			if got := m.Degraded(); got != tc.degraded {
				t.Errorf("Degraded = %v, want %v", got, tc.degraded)
			}
			if tc.degraded || tc.noColor {
				for _, color := range []Color{m.Palette().User, m.Palette().Border, m.Palette().Muted} {
					if color != (Color{}) {
						t.Errorf("degraded palette still has colour %q", color.fg)
					}
				}
			}
			if tc.degraded {
				found := false
				for _, entry := range m.Transcript() {
					if strings.Contains(entry.Text, "colour output degraded") {
						found = true
					}
				}
				if !found {
					t.Error("the degraded notice is missing from the transcript")
				}
			}
		})
	}
}

func TestSetThemeSwitchesPaletteAndFailsOnUnknownName(t *testing.T) {
	m := New(Config{})
	if got := m.Theme(); got != ThemeDark {
		t.Fatalf("default theme = %q, want dark", got)
	}
	before := m.Palette()
	if err := m.SetTheme("light"); err != nil {
		t.Fatalf("SetTheme: %v", err)
	}
	if got := m.Theme(); got != ThemeLight {
		t.Fatalf("theme = %q, want light", got)
	}
	if m.Palette().Border == before.Border {
		t.Error("SetTheme did not swap the palette")
	}
	if err := m.SetTheme("neon"); !errors.Is(err, ErrUnknownTheme) {
		t.Errorf("SetTheme error = %v, want ErrUnknownTheme", err)
	}
	if got := m.Theme(); got != ThemeLight {
		t.Errorf("a failed SetTheme changed the theme to %q", got)
	}
}

func TestStatusLineShowsActiveThemeWhenNotDark(t *testing.T) {
	m := New(Config{Locale: "en"})
	if got := m.Frame(100, 24).RowText(23); strings.Contains(got, "theme") {
		t.Errorf("dark status line should not name the theme: %q", got)
	}
	if err := m.SetTheme(ThemeANSI); err != nil {
		t.Fatal(err)
	}
	if got := m.Frame(100, 24).RowText(23); !strings.Contains(got, "theme ansi") {
		t.Errorf("status line = %q, want the active theme", got)
	}
}
