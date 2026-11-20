package commands

// Rich-text summaries of commands for help screens and completion lists.
//
// FormatCommandSummary is a pure capability: it formats the declarative fields
// of a Command, reads no files, spawns no processes and never mutates cmd.

import (
	"strings"
)

const (
	summarySeparator  = "  " // cells between the command name and its description
	summaryHintIndent = "  " // leading cells of the argument-hint (second) line
)

// FormatCommandSummary renders cmd as a bounded-width, one- or two-line
// summary suitable for a help screen or a completion dropdown.
//
// Layout:
//   - The "head" is cmd.Name followed by cmd.Description, separated by two
//     spaces; empty fields are skipped (a name-only or description-only head is
//     allowed).
//   - When both the head and cmd.ArgumentHint are non-empty, the summary has
//     two lines: the head, then the hint indented by two spaces. Otherwise it
//     has a single line holding whichever part is present; if every field is
//     empty the result is "".
//
// Each line is truncated to at most width terminal cells (display columns).
// Truncation is rune-safe: it never splits a UTF-8 rune and never cuts an East
// Asian wide/full-width rune in half — CJK ideographs, kana, Hangul, fullwidth
// forms and emoji count as two cells and are included only when both cells fit.
// Combining marks and other zero-width runes count as zero cells. Embedded
// newlines and repeated whitespace collapse to single spaces so a field always
// stays on one line. A width <= 0 leaves no room and yields "".
func FormatCommandSummary(cmd Command, width int) string {
	if width <= 0 {
		return ""
	}
	name := summaryField(cmd.Name)
	description := summaryField(cmd.Description)
	hint := summaryField(cmd.ArgumentHint)

	headParts := make([]string, 0, 2)
	if name != "" {
		headParts = append(headParts, name)
	}
	if description != "" {
		headParts = append(headParts, description)
	}
	head := strings.Join(headParts, summarySeparator)

	if head == "" {
		if hint == "" {
			return ""
		}
		return truncateToWidth(hint, width)
	}
	if hint == "" {
		return truncateToWidth(head, width)
	}
	return truncateToWidth(head, width) + "\n" + truncateToWidth(summaryHintIndent+hint, width)
}

// summaryField normalizes one declarative field to single-spaced, single-line
// text so the summary never grows an extra line from the input.
func summaryField(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// stringDisplayWidth returns the number of terminal cells s occupies.
func stringDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

// truncateToWidth returns the longest prefix of s whose display width is at
// most width, cutting only at rune boundaries. Wide runes are never split.
func truncateToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if stringDisplayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeDisplayWidth(r)
		if w+rw > width {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

// runeDisplayWidth is the number of terminal cells a rune occupies: 0 for
// control, combining and zero-width runes, 2 for East Asian wide/full-width
// runes (and emoji), 1 otherwise. The ranges cover the CJK blocks relevant to
// Chinese, Japanese and Korean command descriptions without a dependency.
func runeDisplayWidth(r rune) int {
	switch {
	case r == 0 || r < 32 || (r >= 0x7f && r < 0xa0):
		return 0
	case isZeroWidthRune(r):
		return 0
	case isWideRune(r):
		return 2
	default:
		return 1
	}
}

func isWideRune(r rune) bool {
	return inRanges(r, wideRuneRanges)
}

func isZeroWidthRune(r rune) bool {
	return inRanges(r, zeroWidthRuneRanges)
}

func inRanges(r rune, ranges [][2]rune) bool {
	for _, rng := range ranges {
		if r < rng[0] {
			return false
		}
		if r <= rng[1] {
			return true
		}
	}
	return false
}

// wideRuneRanges is a compact subset of Unicode East Asian Wide/Fullwidth
// blocks (and wide emoji), sorted ascending for inRanges' early exit.
var wideRuneRanges = [][2]rune{
	{0x1100, 0x115F}, // Hangul Jamo initial consonants
	{0x231A, 0x231B}, // watch, hourglass
	{0x2329, 0x232A}, // left/right angle bracket
	{0x2E80, 0x303E}, // CJK radicals, Kangxi, CJK symbols and punctuation (incl. U+3000)
	{0x3041, 0x30FF}, // Hiragana and Katakana
	{0x3100, 0x312F}, // Bopomofo
	{0x3131, 0x318E}, // Hangul compatibility Jamo
	{0x3190, 0x31EF}, // CJK miscellaneous / Bopomofo extended
	{0x31F0, 0x321E}, // Katakana phonetic extensions, enclosed CJK
	{0x3220, 0x3247}, // enclosed ideographs
	{0x3250, 0x4DBF}, // CJK extensions, CJK Ext A
	{0x4E00, 0x9FFF}, // CJK Unified Ideographs
	{0xA000, 0xA48C}, // Yi syllables and radicals
	{0xA960, 0xA97C}, // Hangul Jamo extended A
	{0xAC00, 0xD7A3}, // Hangul syllables
	{0xF900, 0xFAFF}, // CJK compatibility ideographs
	{0xFE10, 0xFE19}, // vertical forms
	{0xFE30, 0xFE6F}, // CJK compatibility forms
	{0xFF01, 0xFF60}, // fullwidth forms (incl. fullwidth ASCII)
	{0xFFE0, 0xFFE6}, // fullwidth signs
	{0x16FE0, 0x16FE4},
	{0x17000, 0x187F7}, // Tangut
	{0x1B000, 0x1B2FB}, // Kana supplement
	{0x1F004, 0x1F004},
	{0x1F0CF, 0x1F0CF},
	{0x1F18E, 0x1F18E},
	{0x1F191, 0x1F19A},
	{0x1F200, 0x1F251},
	{0x1F300, 0x1F5FF}, // miscellaneous symbols and pictographs
	{0x1F600, 0x1F64F}, // emoji
	{0x1F680, 0x1F6FF}, // transport and map
	{0x1F900, 0x1F9FF},
	{0x1FA70, 0x1FAFF},
	{0x20000, 0x3FFFD}, // CJK Ext B and beyond
}

// zeroWidthRuneRanges covers combining marks and format characters that do not
// advance the cursor on their own.
var zeroWidthRuneRanges = [][2]rune{
	{0x0300, 0x036F}, // combining diacritical marks
	{0x0483, 0x0489},
	{0x0591, 0x05BD}, {0x05BF, 0x05BF}, {0x05C1, 0x05C2}, {0x05C4, 0x05C5}, {0x05C7, 0x05C7},
	{0x0610, 0x061A}, {0x064B, 0x065F}, {0x0670, 0x0670}, {0x06D6, 0x06DC}, {0x06DF, 0x06E4},
	{0x06E7, 0x06E8}, {0x06EA, 0x06ED},
	{0x0711, 0x0711}, {0x0730, 0x074A}, {0x07A6, 0x07B0}, {0x07EB, 0x07F3},
	{0x0816, 0x0819}, {0x081B, 0x0823}, {0x0825, 0x0827}, {0x0829, 0x082D},
	{0x0859, 0x085B}, {0x08D3, 0x0902},
	{0x093A, 0x093A}, {0x093C, 0x093C}, {0x0941, 0x0948}, {0x094D, 0x094D},
	{0x0951, 0x0957}, {0x0962, 0x0963},
	{0x1AB0, 0x1AFF},   // combining diacritical marks extended
	{0x1DC0, 0x1DFF},   // combining diacritical marks supplement
	{0x200B, 0x200F},   // zero-width space, ZWNJ, ZWJ, LRM, RLM
	{0x202A, 0x202E},   // bidirectional embedding controls
	{0x2060, 0x206F},   // word joiner / invisible operators
	{0x20D0, 0x20FF},   // combining marks for symbols
	{0xFE00, 0xFE0F},   // variation selectors
	{0xFE20, 0xFE2F},   // combining half marks
	{0xFEFF, 0xFEFF},   // zero-width no-break space
	{0xE0100, 0xE01EF}, // variation selectors supplement
}
