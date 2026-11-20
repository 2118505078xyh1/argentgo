package memory

// Grapheme-cluster-safe clipping, implemented with the standard library only.
//
// A "grapheme cluster" is what a human reads as one character. Splitting a
// string at an arbitrary byte or rune boundary can break a combining sequence
// ("e" + U+0301), a ZWJ emoji (👨‍👩‍👧‍👦), a regional-indicator flag (🇨🇳) or a
// Hangul jamo sequence into invalid or nonsensical fragments. The clippers in
// this file never cut inside such a cluster.
//
// Coverage (pragmatic, stdlib-only):
//   - a base rune plus following Unicode combining marks (Mn/Mc/Me),
//     variation selectors (FE00–FE0F), ZWNJ, emoji skin-tone modifiers and
//     emoji tag characters;
//   - ZWJ (U+200D) chains: the rune after a ZWJ is glued into the cluster;
//   - paired regional indicators (U+1F1E6–U+1F1FF) form a single flag cluster;
//   - Hangul jamo sequences grouped by the L/V/T composition rules;
//   - CRLF kept together.
//
// Honest limitations: this is NOT a complete UAX #29 implementation. It has no
// extended-pictographic / prepend / spacing-mark / Indic virama rules, does not
// implement the full GB9c (Indic conjunct) or GB11 (extended pictographic ZWJ)
// gating, and does not tokenise regional indicators with intervening extenders
// perfectly. It is deliberately conservative: when in doubt it keeps bytes
// together, so it may under-count clusters for exotic scripts but will not emit
// a dangling combining mark or a half emoji. Callers needing full conformance
// should use a dedicated UAX #29 implementation; the stdlib has none.

import (
	"unicode"
	"unicode/utf8"
)

const (
	zwj      = 0x200D  // zero-width joiner: glues the next rune into a cluster
	riStart  = 0x1F1E6 // regional indicator "A"
	riEnd    = 0x1F1FF // regional indicator "Z"
	ellipsis = "…"
)

// ClipGraphemes returns the first maxClusters grapheme clusters of s. The
// result is always a valid UTF-8 string and is never longer than s. Non-positive
// maxClusters yields the empty string.
func ClipGraphemes(s string, maxClusters int) string {
	if maxClusters <= 0 || s == "" {
		return ""
	}
	end := clusterPrefixEnd(s, maxClusters, -1)
	return longestValidPrefix(s[:end])
}

// TruncateGraphemes behaves like ClipGraphemes but appends "…" when the input
// actually had to be shortened. When s already fits in maxClusters clusters it
// is returned unchanged (no ellipsis is added).
func TruncateGraphemes(s string, maxClusters int) string {
	clipped := ClipGraphemes(s, maxClusters)
	if clipped == s {
		return s
	}
	return clipped + ellipsis
}

// clipGraphemeBytes is the byte-budget sibling of ClipGraphemes. It returns the
// longest prefix of s that fits within maxBytes bytes without cutting a
// grapheme cluster in half. Reflect uses it to enforce a byte cap while keeping
// combining sequences and emoji whole. Invalid UTF-8 input is trimmed to its
// longest valid prefix.
func clipGraphemeBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || s == "" {
		return ""
	}
	end := clusterPrefixEnd(s, -1, maxBytes)
	return longestValidPrefix(s[:end])
}

// clusterPrefixEnd walks clusters from the start of s and stops after either
// maxClusters clusters (when >= 0) or maxBytes bytes (when >= 0). The caller
// supplies exactly one limit. It returns a byte offset that is always a cluster
// boundary (and hence a rune boundary for valid UTF-8 input).
func clusterPrefixEnd(s string, maxClusters, maxBytes int) int {
	end := 0
	count := 0
	for i := 0; i < len(s); {
		next := clusterEnd(s, i)
		if maxBytes >= 0 && next > maxBytes {
			break
		}
		i = next
		end = next
		count++
		if maxClusters >= 0 && count >= maxClusters {
			break
		}
	}
	return end
}

// clusterEnd returns the byte offset just past the grapheme cluster that starts
// at byte offset i. It advances at least one rune, so callers always make
// progress even on malformed UTF-8 (where a bad byte decodes as RuneError/size 1).
func clusterEnd(s string, i int) int {
	base, size := utf8.DecodeRuneInString(s[i:])
	j := i + size

	// A CRLF pair is one cluster.
	if base == '\r' && j < len(s) && s[j] == '\n' {
		return j + 1
	}

	// Regional indicators pair up left-to-right to form flags.
	if isRegionalIndicator(base) && j < len(s) {
		if next, sz := utf8.DecodeRuneInString(s[j:]); isRegionalIndicator(next) {
			j += sz
		}
	}

	prev := hangulClass(base)
	for j < len(s) {
		r, sz := utf8.DecodeRuneInString(s[j:])
		switch {
		case r == zwj:
			// ZWJ glues the following rune (and its own extenders) here.
			j += sz
			if j < len(s) {
				glued, gsz := utf8.DecodeRuneInString(s[j:])
				j += gsz
				prev = hangulClass(glued)
			}
		case isClusterExtend(r):
			j += sz
		case hangulGlues(prev, hangulClass(r)):
			prev = hangulClass(r)
			j += sz
		default:
			return j
		}
	}
	return j
}

// isClusterExtend reports whether r attaches to the preceding base rune:
// combining marks, variation selectors, joiners and emoji modifiers.
func isClusterExtend(r rune) bool {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
		return true
	}
	switch {
	case r >= 0xFE00 && r <= 0xFE0F, // variation selectors
		r == 0x200C,                  // ZWNJ
		r >= 0x1F3FB && r <= 0x1F3FF, // emoji skin-tone modifiers
		r >= 0xE0020 && r <= 0xE007F: // emoji tag characters
		return true
	}
	return false
}

func isRegionalIndicator(r rune) bool { return r >= riStart && r <= riEnd }

// jamoClass classifies a rune for the Hangul composition rules (UAX #29 GB6–8).
type jamoClass uint8

const (
	jamoNone jamoClass = iota
	jamoL
	jamoV
	jamoT
	jamoLV
	jamoLVT
)

func hangulClass(r rune) jamoClass {
	switch {
	case r >= 0x1100 && r <= 0x115F: // choseong / leading jamo
		return jamoL
	case r >= 0x1160 && r <= 0x11A7: // jungseong / vowel jamo
		return jamoV
	case r >= 0x11A8 && r <= 0x11C2: // jongseong / trailing jamo
		return jamoT
	case r >= 0xAC00 && r <= 0xD7A3: // precomposed syllable
		if (r-0xAC00)%28 == 0 {
			return jamoLV
		}
		return jamoLVT
	}
	return jamoNone
}

// hangulGlues reports whether next may join the cluster whose last jamo class
// was prev.
func hangulGlues(prev, next jamoClass) bool {
	switch prev {
	case jamoL:
		return next == jamoL || next == jamoV || next == jamoLV || next == jamoLVT
	case jamoLV, jamoV:
		return next == jamoV || next == jamoT
	case jamoLVT, jamoT:
		return next == jamoT
	}
	return false
}

// longestValidPrefix returns the longest prefix of s that is well-formed UTF-8.
// This is a defensive guard for callers that pass malformed input; for valid
// input it returns s unchanged.
func longestValidPrefix(s string) string {
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && sz == 1 {
			return s[:i]
		}
		i += sz
	}
	return s
}
