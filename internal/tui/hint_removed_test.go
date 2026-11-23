package tui

import (
	"strings"
	"testing"
)

func TestDefaultFrameHasNoBottomHint(t *testing.T) {
	m := New(Config{})
	buf := m.Frame(100, 24)
	text := frameText(m, 100, 24)
	// 欢迎卡合法地展示 /help 入口；这里只守护"全局底部 hint bar 不存在"，
	// 因此检查范围限定在 hintY 那一行，而不是整个首屏。
	for _, unwanted := range []string{"enter send", "alt+enter", "pgup/pgdn"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("the default frame still contains %q:\n%s", unwanted, text)
		}
	}
	l := m.layoutLocked(100, 24)
	if row := buf.RowText(l.usageY); strings.TrimSpace(row) == "" {
		t.Fatal("the pi-style usage row must not be blank")
	}
	if l.statusY != 18 || l.ruleY != -1 || l.rootY != -1 || l.usageY != 22 {
		t.Fatalf("layout moved: hintY=%d statusY=%d, want 18/-1/-1/22", l.hintY, l.statusY)
	}
	if got := m.Frame(100, 24).RowText(l.statusY); got == "" {
		t.Fatal("the status line must stay at statusY")
	}
}

func TestHintLineIsEmptyWithoutModal(t *testing.T) {
	// 无审批/对话框时保持空行：用户要求移除全局底部 hint bar。
	for _, locale := range []string{"en", "zh"} {
		if line := hintLine(100, locale, false, Style{}); len(line.Spans) != 0 || line.Width() != 0 {
			t.Fatalf("hintLine(locale=%s, modal=false) = %q, want empty", locale, line.Text())
		}
	}
}

func TestHintLineShowsContextualKeysInModal(t *testing.T) {
	// 审批态按需显示上下文相关按键，而不是恢复全局提示条。
	for _, tc := range []struct {
		locale string
		want   string
	}{
		{"en", "y allow"},
		{"zh", "y 允许"},
	} {
		line := hintLine(100, tc.locale, true, Style{})
		if !strings.Contains(line.Text(), tc.want) {
			t.Fatalf("hintLine(locale=%s, modal=true) = %q, want %q", tc.locale, line.Text(), tc.want)
		}
	}
}
