package tui

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind enumerates the logical keys the UI understands. Escape sequences are
// decoded to this shape so the model never sees raw bytes.
type Kind int

// Key kinds.
const (
	KindRune Kind = iota
	KindEnter
	KindNewline // ctrl+j: insert a line break instead of sending
	KindBackspace
	KindDelete
	KindLeft
	KindRight
	KindUp
	KindDown
	KindHome
	KindEnd
	KindPageUp
	KindPageDown
	KindScrollUp
	KindScrollDown
	KindTab
	KindBackTab
	KindEsc
	KindCtrlC
	KindCtrlA
	KindCtrlE
	KindCtrlK
	KindCtrlU
	KindCtrlW
	KindCtrlL
	KindAltEnter
	KindPaste
	KindIgnore

	// Panel toggles are appended so the numeric values of the keys above stay
	// the same for callers that decode bytes into Kind values directly.
	KindCtrlT // toggle the tasks panel
	KindCtrlB // toggle the workspace panel
	KindCtrlP // toggle the plan panel
	KindCtrlS // toggle the session rail
	KindAltU  // undo last turn
	KindAltR  // retry last turn
)

// Key is one decoded keystroke.
type Key struct {
	Kind Kind
	Rune rune
	Text string // pasted block, for KindPaste
}

// Decoder turns a byte stream into keys. Partial escape sequences are kept
// between reads, so a sequence split across two reads is not lost.
type Decoder struct {
	buf []byte
}

// Pending reports whether an incomplete sequence is buffered.
func (d *Decoder) Pending() bool { return len(d.buf) > 0 }

// Feed appends bytes and returns every complete key they contain.
func (d *Decoder) Feed(p []byte) []Key {
	if len(p) > 0 {
		d.buf = append(d.buf, p...)
	}
	return d.drain()
}

// FlushEscape turns a lone pending ESC into the Esc key. The run loop calls it
// when no further bytes arrive quickly, so a single Esc still aborts at once.
func (d *Decoder) FlushEscape() []Key {
	if len(d.buf) != 1 || d.buf[0] != 0x1b {
		return nil
	}
	d.buf = nil
	return []Key{{Kind: KindEsc}}
}

func (d *Decoder) drain() []Key {
	var keys []Key
	for {
		key, used, ok := decodeKey(d.buf)
		if !ok {
			break
		}
		keys = append(keys, key)
		d.buf = d.buf[used:]
	}
	if len(d.buf) == 0 {
		d.buf = nil
	}
	return keys
}

// decodeKey decodes one key from the front of b. The third result is false
// when b is a prefix of a longer sequence.
func decodeKey(b []byte) (Key, int, bool) {
	if len(b) == 0 {
		return Key{}, 0, false
	}
	if b[0] == 0x1b {
		return decodeEscape(b)
	}
	switch c := b[0]; {
	case c == 0x0d:
		return Key{Kind: KindEnter}, 1, true
	case c == 0x0a:
		return Key{Kind: KindNewline}, 1, true
	case c == 0x7f, c == 0x08:
		return Key{Kind: KindBackspace}, 1, true
	case c == 0x09:
		return Key{Kind: KindTab}, 1, true
	case c == 0x03:
		return Key{Kind: KindCtrlC}, 1, true
	case c == 0x01:
		return Key{Kind: KindCtrlA}, 1, true
	case c == 0x05:
		return Key{Kind: KindCtrlE}, 1, true
	case c == 0x0b:
		return Key{Kind: KindCtrlK}, 1, true
	case c == 0x15:
		return Key{Kind: KindCtrlU}, 1, true
	case c == 0x17:
		return Key{Kind: KindCtrlW}, 1, true
	case c == 0x0c:
		return Key{Kind: KindCtrlL}, 1, true
	case c == 0x14:
		return Key{Kind: KindCtrlT}, 1, true
	case c == 0x02:
		return Key{Kind: KindCtrlB}, 1, true
	case c == 0x10:
		return Key{Kind: KindCtrlP}, 1, true
	case c == 0x13:
		return Key{Kind: KindCtrlS}, 1, true
	case c < 0x20:
		return Key{Kind: KindIgnore}, 1, true
	}
	if !utf8.FullRune(b) {
		return Key{}, 0, false
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && size <= 1 {
		return Key{Kind: KindIgnore}, 1, true
	}
	return Key{Kind: KindRune, Rune: r}, size, true
}

func decodeEscape(b []byte) (Key, int, bool) {
	if len(b) == 1 {
		return Key{}, 0, false // a lone ESC waits for FlushEscape
	}
	switch b[1] {
	case '[', 'O':
	case '\r':
		return Key{Kind: KindAltEnter}, 2, true
	case '\n':
		return Key{Kind: KindAltEnter}, 2, true
	case 'u', 'U':
		return Key{Kind: KindAltU}, 2, true
	case 'r', 'R':
		return Key{Kind: KindAltR}, 2, true
	default:
		// ESC followed by something else: the Esc was pressed on its own and
		// the next byte is a normal key.
		return Key{Kind: KindEsc}, 1, true
	}
	if b[1] == '[' && len(b) >= 6 && bytes.HasPrefix(b[2:], []byte("200~")) {
		rest := b[6:]
		end := bytes.Index(rest, []byte("\x1b[201~"))
		if end < 0 {
			return Key{}, 0, false
		}
		return Key{Kind: KindPaste, Text: string(rest[:end])}, 6 + end + 6, true
	}
	for i := 2; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			return parseCSI(string(b[2:i]), b[i]), i + 1, true
		}
	}
	if len(b) > 32 {
		// A runaway sequence: drop the ESC and carry on instead of growing
		// the buffer forever.
		return Key{Kind: KindIgnore}, 1, true
	}
	return Key{}, 0, false
}

// parseCSI maps the body and final byte of a control sequence to a key.
func parseCSI(body string, final byte) Key {
	switch final {
	case 'A':
		if body == "1;2" {
			return Key{Kind: KindScrollUp}
		}
		return Key{Kind: KindUp}
	case 'B':
		if body == "1;2" {
			return Key{Kind: KindScrollDown}
		}
		return Key{Kind: KindDown}
	case 'C':
		return Key{Kind: KindRight}
	case 'D':
		return Key{Kind: KindLeft}
	case 'H':
		return Key{Kind: KindHome}
	case 'F':
		return Key{Kind: KindEnd}
	case 'Z':
		return Key{Kind: KindBackTab}
	case '~':
		switch body {
		case "", "1", "7":
			return Key{Kind: KindHome}
		case "3":
			return Key{Kind: KindDelete}
		case "4", "8":
			return Key{Kind: KindEnd}
		case "5":
			return Key{Kind: KindPageUp}
		case "6":
			return Key{Kind: KindPageDown}
		}
		return Key{Kind: KindIgnore}
	case 'M', 'm':
		// SGR mouse report: ESC [ < button ; col ; row M. The wheel scrolls.
		if !strings.HasPrefix(body, "<") {
			return Key{Kind: KindIgnore}
		}
		parts := strings.Split(strings.TrimPrefix(body, "<"), ";")
		if len(parts) != 3 {
			return Key{Kind: KindIgnore}
		}
		switch parts[0] {
		case "64":
			return Key{Kind: KindScrollUp}
		case "65":
			return Key{Kind: KindScrollDown}
		}
		return Key{Kind: KindIgnore}
	}
	return Key{Kind: KindIgnore}
}

// KeyBinding is one entry of the footer key bar. Key is the label drawn first
// (for example "ctrl+c"), Desc is its human-readable meaning, and Enabled
// lets a caller hide entries that do not apply to the current UI state without
// rebuilding the whole list.
type KeyBinding struct {
	Key     string
	Desc    string
	Enabled bool
}

const (
	shortHelpSep      = " · "
	shortHelpEllipsis = "…"
)

// HintBindings splits a localised hint string such as
// "enter send · alt+enter newline · /help" into bindings, all enabled. It lets
// callers keep the existing locale data while gaining per-entry control
// (Enabled, order, width-aware rendering) through ShortHelp. A key with no
// description ("/help") is kept as a Key-only binding.
func HintBindings(hint string) []KeyBinding {
	if strings.TrimSpace(hint) == "" {
		return nil
	}
	parts := strings.Split(hint, "·")
	bindings := make([]KeyBinding, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, desc := part, ""
		if i := strings.IndexFunc(part, unicode.IsSpace); i >= 0 {
			key, desc = part[:i], strings.TrimSpace(part[i:])
		}
		if key == "" {
			continue
		}
		bindings = append(bindings, KeyBinding{Key: key, Desc: desc, Enabled: true})
	}
	return bindings
}

// ShortHelp renders bindings in order as "key desc" entries joined by
// shortHelpSep. Disabled entries are skipped. When the joined text does not
// fit in width, whole entries are dropped and a trailing ellipsis is appended
// instead of cutting a key in half, so a narrow terminal never shows a
// silently truncated binding.
//
// textWidth is injected (normally tui.TextWidth) so this helper measures cells
// with the caller's width model; a nil callback falls back to TextWidth. The
// returned string never occupies more than width cells when textWidth is the
// package's TextWidth.
func ShortHelp(bindings []KeyBinding, width int, textWidth func(string) int) string {
	if width <= 0 {
		return ""
	}
	if textWidth == nil {
		textWidth = TextWidth
	}

	segments := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if !binding.Enabled {
			continue
		}
		var segment string
		switch {
		case binding.Key == "" && binding.Desc == "":
			continue
		case binding.Key == "":
			segment = binding.Desc
		case binding.Desc == "":
			segment = binding.Key
		default:
			segment = binding.Key + " " + binding.Desc
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return ""
	}

	full := strings.Join(segments, shortHelpSep)
	if textWidth(full) <= width {
		return full
	}

	// Reserve room for the ellipsis and keep only whole segments that fit.
	limit := width - textWidth(shortHelpEllipsis)
	if limit < 0 {
		limit = 0
	}
	sepWidth := textWidth(shortHelpSep)
	var out strings.Builder
	used := 0
	for i, segment := range segments {
		w := textWidth(segment)
		if i > 0 {
			w += sepWidth
		}
		if used+w > limit {
			break
		}
		if i > 0 {
			out.WriteString(shortHelpSep)
			used += sepWidth
		}
		out.WriteString(segment)
		used += textWidth(segment)
	}
	return out.String() + shortHelpEllipsis
}
