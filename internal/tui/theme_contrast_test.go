package tui

import (
	"strings"
	"testing"
)

// 低对比自定义配色的固定取值：白底上浅灰原墨色不足 3:1，
// 备选深灰超过 3:1，因此 LegibleInk 必须切换到备选。
const (
	badgeTestBG  = "#FFFFFF"
	badgeTestInk = "#B0B0B0"
	badgeTestAlt = "#767676"
)

// TestCustomBadgeThemeUsesLegibleAltInk 构造低对比的自定义徽章主题：
// 先断言底层原语切换到高对比备选，再断言接线后的渲染也确实采用
// 备选墨色的 RGB，而不是原来的浅灰。
func TestCustomBadgeThemeUsesLegibleAltInk(t *testing.T) {
	if inkRatio := ContrastRatio(badgeTestBG, badgeTestInk); inkRatio >= minInkContrast {
		t.Fatalf("fixture ink ratio = %.4f, want < %.1f", inkRatio, minInkContrast)
	}
	if altRatio := ContrastRatio(badgeTestBG, badgeTestAlt); altRatio <= minInkContrast {
		t.Fatalf("fixture alt ratio = %.4f, want > %.1f", altRatio, minInkContrast)
	}
	if got := LegibleInk(badgeTestBG, badgeTestInk, badgeTestAlt); got != badgeTestAlt {
		t.Fatalf("LegibleInk = %q, want alt %q", got, badgeTestAlt)
	}

	m := New(Config{Locale: "en"})
	m.palette.BadgeBGHex = badgeTestBG
	m.palette.BadgeInkHex = badgeTestInk
	m.palette.BadgeAltInkHex = badgeTestAlt

	// 辅助函数只替换前景，保留 base 的其余风格。
	base := m.palette.warning()
	got := m.legibleBadgeStyle(base)
	r, g, b, ok := parseHexColor(badgeTestAlt)
	if !ok {
		t.Fatal("test fixture alt is not a valid hex colour")
	}
	wantColor := RGB(r, g, b)
	if got.Fg != wantColor {
		t.Errorf("badge ink = %q, want alt RGB %q", got.Fg.fg, wantColor.fg)
	}

	// 端到端：尝试徽章与通知条目都应写出备选墨色的 RGB 前景，
	// 且不再出现原墨色的 RGB 转义。
	m.AppendAttempt(AttemptFailed, "detail")
	m.AppendNotice("notice")
	var out strings.Builder
	if _, err := m.Frame(80, 24).Flush(&out); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if written := out.String(); !strings.Contains(written, wantColor.fg) {
		t.Errorf("frame does not use the legible alt ink %q:\n%s", wantColor.fg, written)
	}
	faintR, faintG, faintB, _ := parseHexColor(badgeTestInk)
	if faint := RGB(faintR, faintG, faintB).fg; strings.Contains(out.String(), faint) {
		t.Errorf("frame still uses the faint ink %q", faint)
	}
}

// TestBuiltInThemesKeepBadgeFramesByteIdentical 验证三个内置主题的
// hex 字段全为空，且在这一配置下徽章/通知走原 SGR 路径：显式清空
// hex 前后刷新出的整帧字节完全相同，也不会泄漏任何 RGB 转义。
func TestBuiltInThemesKeepBadgeFramesByteIdentical(t *testing.T) {
	for _, name := range ThemeNames() {
		t.Run(name, func(t *testing.T) {
			p, err := paletteFor(name)
			if err != nil {
				t.Fatalf("paletteFor(%q): %v", name, err)
			}
			if p.BadgeBGHex != "" || p.BadgeInkHex != "" || p.BadgeAltInkHex != "" {
				t.Fatalf("built-in theme %q must leave badge hex fields empty", name)
			}

			m := New(Config{Theme: name, Locale: "en"})
			m.AppendAttempt(AttemptFailed, "detail")
			m.AppendNotice("notice")
			if base := p.warning(); m.legibleBadgeStyle(base) != base {
				t.Errorf("%s: legibleBadgeStyle changed the SGR base style", name)
			}

			var golden strings.Builder
			if _, err := m.Frame(80, 24).Flush(&golden); err != nil {
				t.Fatalf("flush: %v", err)
			}
			before := golden.String()
			if strings.Contains(before, "38;2;") || strings.Contains(before, "48;2;") {
				t.Errorf("%s: built-in frame leaked an RGB escape", name)
			}

			// 再显式清空（本就为空）并重新渲染，字节必须一致。
			m.palette.BadgeBGHex, m.palette.BadgeInkHex, m.palette.BadgeAltInkHex = "", "", ""
			m.version++
			var after strings.Builder
			if _, err := m.Frame(80, 24).Flush(&after); err != nil {
				t.Fatalf("flush: %v", err)
			}
			if after.String() != before {
				t.Errorf("%s: frame changed with empty hex:\nbefore %q\nafter  %q", name, before, after.String())
			}
		})
	}
}

// TestContrastRatioBoundaries 守住 WCAG 比率的两端：
// 相同颜色为 1，黑白互为 21。
func TestContrastRatioBoundaries(t *testing.T) {
	if got := ContrastRatio("#123456", "#123456"); got != 1 {
		t.Errorf("identical colours ratio = %v, want 1", got)
	}
	if got := ContrastRatio("#000000", "#FFFFFF"); got != 21 {
		t.Errorf("black-on-white ratio = %v, want 21", got)
	}
	if got := ContrastRatio("#FFFFFF", "#000000"); got != 21 {
		t.Errorf("white-on-black ratio = %v, want 21", got)
	}
}
