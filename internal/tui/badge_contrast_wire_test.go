package tui

import (
	"sync/atomic"
	"testing"
)

func TestBadgeContrastRoles(t *testing.T) {
	env := func(k string) string {
		switch k {
		case badgeBGEnv:
			return "#1a1a2e"
		case badgeInkEnv:
			return "#ffffff"
		case badgeAltInkEnv:
			return "#000000"
		}
		return ""
	}
	bg, ink, alt := badgeContrastRoles(env)
	if bg != "#1a1a2e" || ink != "#ffffff" || alt != "#000000" {
		t.Fatalf("roles = %q %q %q", bg, ink, alt)
	}
	bg, ink, alt = badgeContrastRoles(func(string) string { return "" })
	if bg != "" || ink != "" || alt != "" {
		t.Fatal("empty env should yield empty roles")
	}
}

func TestApplyBadgeOverride(t *testing.T) {
	// 无覆盖时保持原样（内置主题 hex 为空）。
	badgeOverride = atomic.Value{} // reset
	p := Palette{}
	got := applyBadgeOverride(p)
	if got.BadgeBGHex != "" {
		t.Fatalf("unexpected override: %+v", got)
	}
	badgeOverride.Store(paletteBadgeOverride{BG: "#111", Ink: "#eee", AltInk: "#000"})
	got = applyBadgeOverride(Palette{})
	if got.BadgeBGHex != "#111" || got.BadgeInkHex != "#eee" || got.BadgeAltInkHex != "#000" {
		t.Fatalf("override not applied: %+v", got)
	}
	badgeOverride = atomic.Value{} // cleanup
}
