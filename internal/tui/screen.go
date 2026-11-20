package tui

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Color is a foreground/background SGR pair. The zero value is the terminal
// default colour, which is what most text uses.
type Color struct {
	fg string
	bg string
}

// The eight base colours plus the bright grey/white pair, mirroring the Rust
// screen model (`crates/core/src/screen.rs`).
var (
	ColorDefault = Color{}
	ColorRed     = Color{"31", "41"}
	ColorGreen   = Color{"32", "42"}
	ColorYellow  = Color{"33", "43"}
	ColorBlue    = Color{"34", "44"}
	ColorMagenta = Color{"35", "45"}
	ColorCyan    = Color{"36", "46"}
	ColorGray    = Color{"90", "100"}
	ColorWhite   = Color{"97", "107"}
)

// Fixed returns a colour from the 256-colour palette.
func Fixed(n uint8) Color {
	index := strconv.Itoa(int(n))
	return Color{"38;5;" + index, "48;5;" + index}
}

// RGB returns a 24-bit colour.
func RGB(r, g, b uint8) Color {
	body := strconv.Itoa(int(r)) + ";" + strconv.Itoa(int(g)) + ";" + strconv.Itoa(int(b))
	return Color{"38;2;" + body, "48;2;" + body}
}

// Style is how a span is drawn.
type Style struct {
	Fg      Color
	Bg      Color
	Bold    bool
	Dim     bool
	Reverse bool
}

// SGR is the escape sequence that turns the style on, or "" for no styling.
func (s Style) SGR() string {
	var out strings.Builder
	sep := false
	add := func(code string) {
		if sep {
			out.WriteByte(';')
		}
		out.WriteString(code)
		sep = true
	}
	if s.Fg.fg != "" {
		add(s.Fg.fg)
	}
	if s.Bold {
		add("1")
	}
	if s.Dim {
		add("2")
	}
	if s.Reverse {
		add("7")
	}
	if s.Bg.bg != "" {
		add(s.Bg.bg)
	}
	if !sep {
		return ""
	}
	return "\x1b[" + out.String() + "m"
}

// BoldStyle returns a bold style in the given colour.
func BoldStyle(c Color) Style { return Style{Fg: c, Bold: true} }

// Span is a run of text with one style.
type Span struct {
	Text  string
	Style Style
}

// Width is how many cells the span covers.
func (s Span) Width() int { return TextWidth(s.Text) }

// Line is one rendered row.
type Line struct {
	Spans []Span
}

// NewLine starts an empty line.
func NewLine() Line { return Line{} }

// PlainLine is a line of unstyled text.
func PlainLine(text string) Line { return Line{Spans: []Span{{Text: text}}} }

// Push appends styled text.
func (l *Line) Push(text string, style Style) {
	if text == "" {
		return
	}
	l.Spans = append(l.Spans, Span{Text: text, Style: style})
}

// PushSpan appends a span.
func (l *Line) PushSpan(s Span) {
	if s.Text == "" {
		return
	}
	l.Spans = append(l.Spans, s)
}

// Width is the number of cells the line covers.
func (l Line) Width() int {
	total := 0
	for _, span := range l.Spans {
		total += span.Width()
	}
	return total
}

// Text is the unstyled content of the line.
func (l Line) Text() string {
	var out strings.Builder
	for _, span := range l.Spans {
		out.WriteString(span.Text)
	}
	return out.String()
}

// Fill pads the line with styled spaces until it covers width cells.
func (l *Line) Fill(width int, style Style) {
	if used := l.Width(); used < width {
		l.Spans = append(l.Spans, Span{Text: strings.Repeat(" ", width-used), Style: style})
	}
}

// RuneWidth is the number of terminal cells one rune occupies.
//
// Zero-width: combining marks and variation selectors. Two cells: the East
// Asian ranges and emoji that actually show up in prose and paths. Everything
// printable else is one cell. This is the same approximation as the Rust
// reference table, kept small on purpose.
func RuneWidth(r rune) int {
	code := int32(r)
	switch {
	case code >= 0x0300 && code <= 0x036F,
		code >= 0x0483 && code <= 0x0489,
		code >= 0x200B && code <= 0x200F,
		code >= 0x20D0 && code <= 0x20FF,
		code >= 0xFE00 && code <= 0xFE0F,
		code >= 0xFE20 && code <= 0xFE2F,
		code >= 0xE0100 && code <= 0xE01EF:
		return 0
	}
	if code < 0x20 || code == 0x7F {
		return 0
	}
	switch {
	case code >= 0x1100 && code <= 0x115F,
		code >= 0x2E80 && code <= 0x303E,
		code >= 0x3041 && code <= 0x33FF,
		code >= 0x3400 && code <= 0x4DBF,
		code >= 0x4E00 && code <= 0x9FFF,
		code >= 0xA000 && code <= 0xA4CF,
		code >= 0xA960 && code <= 0xA97F,
		code >= 0xAC00 && code <= 0xD7A3,
		code >= 0xF900 && code <= 0xFAFF,
		code >= 0xFE10 && code <= 0xFE19,
		code >= 0xFE30 && code <= 0xFE6F,
		code >= 0xFF00 && code <= 0xFF60,
		code >= 0xFFE0 && code <= 0xFFE6,
		code >= 0x1F300 && code <= 0x1F64F,
		code >= 0x1F680 && code <= 0x1F6FF,
		code >= 0x1F900 && code <= 0x1F9FF,
		code >= 0x1FA70 && code <= 0x1FAFF,
		code >= 0x20000 && code <= 0x3FFFD:
		return 2
	}
	return 1
}

const zwj = 0x200D

// isRegionalIndicator reports whether r is one of the paired flag letters.
func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

// isExtend reports whether r attaches to the rune before it in the same
// grapheme cluster (combining marks, variation selectors, ZWJ, emoji
// skin-tone modifiers, ...). C0 controls and DEL are deliberately excluded:
// they are zero-width in RuneWidth but must not glue two clusters together.
func isExtend(r rune) bool {
	if r >= 0x1F3FB && r <= 0x1F3FF { // emoji skin-tone modifiers
		return true
	}
	return r >= 0x20 && r != 0x7F && RuneWidth(r) == 0
}

// nextGrapheme returns the byte range [start, end) and display width of the
// grapheme cluster beginning at start in s. This is a deliberately small
// approximation of UAX #29 extended grapheme clusters, covering the cases
// that matter for terminal cells:
//
//   - Base + (Extend | ZWJ Base)* is one cluster whose width is the widest
//     rune in it. A ZWJ pulls the following pictograph into the same cluster,
//     so 👨‍👩‍👧 measures two cells instead of six.
//   - Two adjacent regional indicators form one flag, always two cells.
//   - ASCII is a single-byte, single-cell cluster (fast path).
//
// RuneWidth stays the authority for individual rune widths.
func nextGrapheme(s string, start int) (end, width int) {
	if b := s[start]; b < utf8.RuneSelf {
		if b >= 0x20 && b != 0x7F {
			return start + 1, 1
		}
		return start + 1, 0
	}

	r, size := utf8.DecodeRuneInString(s[start:])
	end = start + size
	width = RuneWidth(r)

	// A pair of adjacent regional indicators is one flag: two cells, even
	// though each indicator alone is only one cell wide.
	if isRegionalIndicator(r) && end < len(s) {
		if r2, size2 := utf8.DecodeRuneInString(s[end:]); isRegionalIndicator(r2) {
			end += size2
			width = 2
			for end < len(s) {
				r3, size3 := utf8.DecodeRuneInString(s[end:])
				if !isExtend(r3) {
					break
				}
				end += size3
			}
			return end, width
		}
	}

	afterZWJ := false
	for end < len(s) {
		r2, size2 := utf8.DecodeRuneInString(s[end:])
		switch {
		case r2 == zwj:
			afterZWJ = true
		case isExtend(r2):
			// An extender stays in the current cluster.
		case afterZWJ && r2 >= 0x20 && r2 != 0x7F:
			// A base joined to the cluster by a preceding ZWJ.
			afterZWJ = false
		default:
			return end, width
		}
		if w := RuneWidth(r2); w > width {
			width = w
		}
		end += size2
	}
	return end, width
}

// TextWidth is the number of cells a string occupies, counted in grapheme
// clusters so a ZWJ emoji or a flag is never double-counted.
func TextWidth(text string) int {
	total := 0
	for i := 0; i < len(text); {
		if text[i] < utf8.RuneSelf {
			// Bulk ASCII path: printable bytes are one cell, controls zero.
			for i < len(text) && text[i] < utf8.RuneSelf {
				if text[i] >= 0x20 && text[i] != 0x7F {
					total++
				}
				i++
			}
			continue
		}
		next, width := nextGrapheme(text, i)
		total += width
		i = next
	}
	return total
}

// Clip cuts text so it occupies at most max cells. A cluster that would
// straddle the boundary is dropped whole, so ZWJ emoji, flags and combining
// sequences are never split down the middle.
func Clip(text string, max int) string {
	if max <= 0 {
		return ""
	}
	end, used := 0, 0
	for pos := 0; pos < len(text); {
		next, width := nextGrapheme(text, pos)
		if used+width > max {
			break
		}
		used += width
		end = next
		pos = next
	}
	return text[:end]
}

// Truncate clips text and marks the cut with an ellipsis.
func Truncate(text string, max int) string {
	if TextWidth(text) <= max {
		return text
	}
	if max <= 0 {
		return ""
	}
	return Clip(text, max-1) + "…"
}

// Pad clips or pads text so it covers exactly width cells.
func Pad(text string, width int) string {
	clipped := Clip(text, width)
	if used := TextWidth(clipped); used < width {
		return clipped + strings.Repeat(" ", width-used)
	}
	return clipped
}

// Wrap breaks text into lines of at most width cells, preferring a space in
// the tail of the line. CJK has no spaces, so the hard break is the normal
// path for Chinese rather than an edge case. Line breaks happen on grapheme
// boundaries: a ZWJ emoji, a flag or a combining sequence is never folded in
// the middle.
func Wrap(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	var out []string
	curStart, curEnd, curWidth := 0, 0, 0
	lastSpace := -1
	for pos := 0; pos < len(text); {
		next, cell := nextGrapheme(text, pos)
		r, _ := utf8.DecodeRuneInString(text[pos:next])
		if curWidth+cell > width && curEnd > curStart {
			if lastSpace > curStart {
				head := strings.TrimRight(text[curStart:lastSpace], " ")
				out = append(out, head)
				tail := lastSpace
				for tail < curEnd && text[tail] == ' ' {
					tail++
				}
				curStart = tail
				curWidth = TextWidth(text[curStart:curEnd])
			} else {
				out = append(out, text[curStart:curEnd])
				curStart = pos
				curWidth = 0
			}
			lastSpace = -1
		}
		curEnd = next
		curWidth += cell
		if r == ' ' {
			lastSpace = pos
		}
		pos = next
	}
	if curEnd > curStart {
		out = append(out, text[curStart:curEnd])
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// compactCount renders a token count as 942 / 8.2k / 1.6M.
func compactCount(n int) string {
	if n < 0 {
		n = 0
	}
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		k := float64(n) / 1000
		if k >= 10 {
			return strconv.Itoa(int(k)) + "k"
		}
		return strconv.FormatFloat(k, 'f', 1, 64) + "k"
	}
	m := float64(n) / 1_000_000
	if m >= 10 {
		return strconv.Itoa(int(m)) + "M"
	}
	return strconv.FormatFloat(m, 'f', 1, 64) + "M"
}

// clock renders elapsed time as 0:07 / 12:04 / 1:02:03.
func clock(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 3600 {
		return strconv.Itoa(seconds/60) + ":" + twoDigits(seconds%60)
	}
	return strconv.Itoa(seconds/3600) + ":" + twoDigits((seconds%3600)/60) + ":" + twoDigits(seconds%60)
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}
