package tui

import "math"

// minInkContrast is the WCAG AA floor for large/bold text (3:1). Badge labels
// are short and bold, so this is the ratio an ink must clear before we leave
// it alone.
const minInkContrast = 3.0

// parseHexColor decodes #rgb, #rgba, #rrggbb and #rrggbbaa into 8-bit
// channels. Alpha, when present, must still be well formed but is ignored:
// luminance is defined on opaque colours and the renderer paints badges over
// the opaque terminal row. The bool reports whether the input was one of the
// supported, well-formed forms.
func parseHexColor(s string) (r, g, b uint8, ok bool) {
	if len(s) == 0 || s[0] != '#' {
		return 0, 0, 0, false
	}
	digits := s[1:]
	switch len(digits) {
	case 3:
		// Expand #abc to #aabbcc so every form below is at least six wide.
		digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2]})
	case 4:
		// Expand #abcd to #aabbccdd; the last pair is validated but unused.
		digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2], digits[3], digits[3]})
	case 6, 8: // 8 is rrggbbaa; the trailing pair is validated but unused.
	default:
		return 0, 0, 0, false
	}

	nibble := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10, true
		default:
			return 0, false
		}
	}
	channel := func(hi, lo byte) (uint8, bool) {
		h, okH := nibble(hi)
		l, okL := nibble(lo)
		if !okH || !okL {
			return 0, false
		}
		return h<<4 | l, true
	}
	if r, ok = channel(digits[0], digits[1]); !ok {
		return 0, 0, 0, false
	}
	if g, ok = channel(digits[2], digits[3]); !ok {
		return 0, 0, 0, false
	}
	if b, ok = channel(digits[4], digits[5]); !ok {
		return 0, 0, 0, false
	}
	if len(digits) == 8 {
		if _, ok = nibble(digits[6]); !ok {
			return 0, 0, 0, false
		}
		if _, ok = nibble(digits[7]); !ok {
			return 0, 0, 0, false
		}
	}
	return r, g, b, true
}

// relativeLuminance is the WCAG 2.0 relative luminance of an opaque sRGB
// colour: linearise each channel, then apply the sRGB luma weights.
func relativeLuminance(r, g, b uint8) float64 {
	linear := func(v uint8) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

// contrast computes the WCAG ratio without exposing a success flag. The bool
// is false when either colour is not a supported hex form.
func contrast(hexA, hexB string) (float64, bool) {
	ar, ag, ab, aok := parseHexColor(hexA)
	br, bgG, bb, bok := parseHexColor(hexB)
	if !aok || !bok {
		return 0, false
	}
	la, lb := relativeLuminance(ar, ag, ab), relativeLuminance(br, bgG, bb)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), true
}

// ContrastRatio returns the WCAG contrast ratio between two hex colours, in
// [0, 21]. Black on white is 21 and a colour against itself is 1. Invalid
// input yields 0, the conservative value: a caller thresholding on it treats
// the pair as illegible rather than guessing.
func ContrastRatio(hexA, hexB string) float64 {
	ratio, ok := contrast(hexA, hexB)
	if !ok {
		return 0
	}
	return ratio
}

// LegibleInk chooses the badge ink to paint on bg. The original ink is kept
// when it already clears minInkContrast, when alt is not strictly better, or
// when any colour is unparseable — so callers that cannot trust their inputs
// never change what they were going to draw.
//
// The built-in palettes store SGR codes rather than hex, so this helper is not
// yet wired into the badge renderer; see the wiring note at the end of file.
func LegibleInk(bg, ink, alt string) string {
	inkRatio, ok := contrast(bg, ink)
	if !ok || inkRatio >= minInkContrast {
		return ink
	}
	altRatio, ok := contrast(bg, alt)
	if !ok || altRatio <= inkRatio {
		return ink
	}
	return alt
}

// Wiring note (deliberately not applied here): Palette roles in theme.go are
// SGR strings such as "33"/"43", not hex, and there is no per-theme badge
// background role to compare against. Applying LegibleInk at render time first
// needs a hex ink/bg pair on Palette populated only for non-built-in themes;
// the call sites would then be Model.attemptLinesLocked (render.go, the "⟳"
// attempt badge) and the notice/banner ink selection in renderEntryLocked.
// Built-in palettes keep their SGR colours, so the renderer is untouched and
// every shipped theme's output stays identical.
