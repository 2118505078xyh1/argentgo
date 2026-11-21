package tui

import (
	"strings"
	"testing"
)

func TestDefaultFrameHasNoBottomHint(t *testing.T) {
	m := New(Config{})
	buf := m.Frame(100, 24)
	text := frameText(m, 100, 24)
	for _, unwanted := range []string{"enter send", "/help", "alt+enter", "pgup/pgdn"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("the default frame still contains %q:\n%s", unwanted, text)
		}
	}
	l := m.layoutLocked(100, 24)
	if row := buf.RowText(l.hintY); strings.TrimSpace(row) != "" {
		t.Fatalf("hintY row is not blank: %q", row)
	}
	if l.statusY != 23 || l.hintY != 22 {
		t.Fatalf("layout moved: hintY=%d statusY=%d, want 22/23", l.hintY, l.statusY)
	}
	if got := m.Frame(100, 24).RowText(l.statusY); got == "" {
		t.Fatal("the status line must stay at statusY")
	}
}

func TestHintLineIsEmptyForAllModes(t *testing.T) {
	for _, modal := range []bool{false, true} {
		for _, locale := range []string{"en", "zh"} {
			if line := hintLine(100, locale, modal, Style{}); len(line.Spans) != 0 || line.Width() != 0 {
				t.Fatalf("hintLine(locale=%s, modal=%v) = %q, want empty", locale, modal, line.Text())
			}
		}
	}
}
