package tui

import (
	"math"
	"testing"
)

func closeTo(got, want float64) bool { return math.Abs(got-want) < 0.001 }

func TestContrastRatio(t *testing.T) {
	tests := []struct {
		name, a, b string
		want       float64
	}{
		{"black on white", "#000000", "#FFFFFF", 21},
		{"white on black", "#FFFFFF", "#000000", 21},
		{"identical colours", "#123456", "#123456", 1},
		{"red on black", "#FF0000", "#000000", 5.252},
		{"yellow on black", "#FFFF00", "#000000", 19.556},
		{"blue on white", "#0000FF", "#FFFFFF", 8.5925},
		{"shorthand matches longhand", "#fff", "#FFFFFF", 1},
		{"mixed case hex", "#aAbBcC", "#AABBCC", 1},
		{"alpha channel ignored", "#00000080", "#000000", 1},
		{"shorthand with alpha ignored", "#fff8", "#FFFFFF", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ContrastRatio(tc.a, tc.b)
			if !closeTo(got, tc.want) {
				t.Errorf("ContrastRatio(%q, %q) = %.4f, want %.4f", tc.a, tc.b, got, tc.want)
			}
			if rev := ContrastRatio(tc.b, tc.a); !closeTo(rev, tc.want) {
				t.Errorf("ratio is not symmetric: reversed = %.4f, want %.4f", rev, tc.want)
			}
		})
	}
}

func TestContrastRatioInvalidColoursAreConservative(t *testing.T) {
	tests := []struct {
		name, a, b string
	}{
		{"empty", "", "#ffffff"},
		{"missing hash", "ffffff", "#ffffff"},
		{"hash only", "#", "#ffffff"},
		{"one digit", "#f", "#ffffff"},
		{"five digits", "#12345", "#ffffff"},
		{"seven digits", "#1234567", "#ffffff"},
		{"non hex digits", "#gg0000", "#ffffff"},
		{"bad alpha", "#123456zz", "#ffffff"},
		{"leading space", " #ffffff", "#000000"},
		{"trailing space", "#ffffff ", "#000000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContrastRatio(tc.a, tc.b); got != 0 {
				t.Errorf("ContrastRatio(%q, %q) = %v, want conservative 0", tc.a, tc.b, got)
			}
			if got := ContrastRatio(tc.b, tc.a); got != 0 {
				t.Errorf("reversed ContrastRatio(%q, %q) = %v, want conservative 0", tc.b, tc.a, got)
			}
		})
	}
}

func TestLegibleInkSwitchesOnlyWhenAltIsBetterAndInkIsFaint(t *testing.T) {
	tests := []struct {
		name, bg, ink, alt, want string
	}{
		{"faint ink switches to legible alt", "#FFFFFF", "#B0B0B0", "#767676", "#767676"},
		{"short hex forms switch too", "#fff", "#b0b0b0", "#767676", "#767676"},
		{"already legible ink is kept", "#FFFFFF", "#000000", "#333333", "#000000"},
		{"alt is not better", "#FFFFFF", "#B0B0B0", "#C0C0C0", "#B0B0B0"},
		{"alt merely ties", "#FFFFFF", "#B0B0B0", "#B0B0B0", "#B0B0B0"},
		{"invalid background", "nope", "#B0B0B0", "#000000", "#B0B0B0"},
		{"invalid ink", "#FFFFFF", "nope", "#000000", "nope"},
		{"invalid alt", "#FFFFFF", "#B0B0B0", "nope", "#B0B0B0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LegibleInk(tc.bg, tc.ink, tc.alt); got != tc.want {
				t.Errorf("LegibleInk(%q, %q, %q) = %q, want %q", tc.bg, tc.ink, tc.alt, got, tc.want)
			}
		})
	}
}

func TestLegibleInkLeavesBuiltInBadgeInksAlone(t *testing.T) {
	// The shipped themes are already high contrast: dark/ansi paint their
	// warning badge yellow on a dark row (~19.56:1) and light paints it red on
	// white (~4:1). Both clear the 3:1 floor, so once a hex-capable palette is
	// wired in, these choices must stay byte-identical.
	tests := []struct {
		name, bg, ink, alt string
	}{
		{"dark warning", "#000000", "#FFFF00", "#FFFFFF"},
		{"light warning", "#FFFFFF", "#FF0000", "#000000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LegibleInk(tc.bg, tc.ink, tc.alt); got != tc.ink {
				t.Errorf("built-in badge ink changed from %q to %q", tc.ink, got)
			}
		})
	}
}
