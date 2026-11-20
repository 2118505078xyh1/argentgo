package tui

// MarkdownStreamBuf holds back the tail of a streamed assistant reply until
// the markdown constructs it might start are complete. Deltas can split a
// `**`, a backtick or a ``` fence at any byte; rendering such a prefix makes
// the marker flicker as it briefly appears and then vanishes. Push returns the
// prefix that is safe to render, and keeps everything else for the next delta.
// Flush releases the remainder when the turn ends.
//
// The buffer is deliberately stateless apart from the withheld text: every
// Push re-derives the fence / inline-code / emphasis state from the pending
// bytes, so a cut can only land where no construct is open.
type MarkdownStreamBuf struct {
	pending string
}

// Push adds a delta and returns the portion safe to render right now. Normal
// text passes straight through; only a possible marker prefix is withheld.
func (b *MarkdownStreamBuf) Push(delta string) string {
	if delta == "" {
		return ""
	}
	b.pending += delta
	cut := commitFrontier(b.pending)
	if cut == 0 {
		return ""
	}
	out := b.pending[:cut]
	b.pending = b.pending[cut:]
	return out
}

// Flush releases every withheld byte, for example when the turn ends.
func (b *MarkdownStreamBuf) Flush() string {
	out := b.pending
	b.pending = ""
	return out
}

// Reset drops any withheld tail without emitting it.
func (b *MarkdownStreamBuf) Reset() {
	b.pending = ""
}

func isMarkdownMarker(c rune) bool {
	return c == '*' || c == '_' || c == '`'
}

// commitFrontier returns the byte index in s before the first incomplete
// markdown construct. Everything before that index renders without flicker.
func commitFrontier(s string) int {
	r := []rune(s)
	n := len(r)
	if n == 0 {
		return 0
	}
	bol := 0 // rune index of the current line's first character
	i := 0
	// advance moves i forward and keeps bol in sync with crossed newlines.
	advance := func(to int) {
		for ; i < to; i++ {
			if r[i] == '\n' {
				bol = i + 1
			}
		}
	}
	consumed := 0 // rune index just past the last fully consumed construct
	for i < n {
		// A fence is a run of >=3 backticks at the start of a line (after
		// optional whitespace), matching markdownLines' TrimSpace + HasPrefix.
		if lineStartWhitespace(r, bol, i) {
			k := i
			for k < n && (r[k] == ' ' || r[k] == '\t') {
				k++
			}
			if k < n && r[k] == '`' {
				if run := backtickRun(r, k); run >= 3 {
					if close := findFenceClose(r, k+run); close >= 0 {
						advance(close)
						consumed = i
						continue
					}
					// Fence opened but not closed: hold from its delimiter.
					return runeByteOffset(r, k)
				}
			}
		}
		switch r[i] {
		case '`':
			if end := findInlineClose(r, i); end >= 0 {
				advance(end + 1)
				consumed = i
				continue
			}
			return runeByteOffset(r, i) // inline code not closed yet
		case '*':
			if i+1 < n && r[i+1] == '*' {
				if end := findPairClose(r, i+2, '*'); end >= 0 {
					advance(end + 2)
					consumed = i
					continue
				}
				return runeByteOffset(r, i) // bold not closed yet
			}
		case '_':
			if i+1 < n && r[i+1] == '_' {
				if end := findPairClose(r, i+2, '_'); end >= 0 {
					advance(end + 2)
					consumed = i
					continue
				}
				return runeByteOffset(r, i) // underscore emphasis not closed yet
			}
		}
		i++
		if i < n && r[i-1] == '\n' {
			bol = i
		}
	}
	// No construct is open. Still withhold a trailing run of one or two marker
	// characters: it could be the start of a `**`, an inline code span or a
	// fence once the next delta arrives. A run of >=3 is already a complete
	// fence marker, so it can be committed.
	start := trailingMarkerRun(r)
	if start < n && n-start <= 2 && start >= consumed {
		return runeByteOffset(r, start)
	}
	return len(s)
}

// lineStartWhitespace reports whether r[bol:i] is empty or only spaces/tabs,
// i.e. i is the first non-blank rune of its line.
func lineStartWhitespace(r []rune, bol, i int) bool {
	for j := bol; j < i; j++ {
		if r[j] != ' ' && r[j] != '\t' {
			return false
		}
	}
	return true
}

func backtickRun(r []rune, i int) int {
	j := i
	for j < len(r) && r[j] == '`' {
		j++
	}
	return j - i
}

// findFenceClose returns the rune index just past the next line's opening
// backtick run of >=3, i.e. the fence closer. Scanning starts on the line
// after from, matching the renderer's line-by-line toggle.
func findFenceClose(r []rune, from int) int {
	n := len(r)
	i := from
	for i < n && r[i] != '\n' {
		i++
	}
	if i < n {
		i++
	}
	for i < n {
		k := i
		for k < n && (r[k] == ' ' || r[k] == '\t') {
			k++
		}
		if k < n && r[k] == '`' {
			if run := backtickRun(r, k); run >= 3 {
				return k + run
			}
		}
		for i < n && r[i] != '\n' {
			i++
		}
		if i < n {
			i++
		}
	}
	return -1
}

// findInlineClose finds a closing backtick with at least one rune of content
// between it and the opener at from, matching parseInline.
func findInlineClose(r []rune, from int) int {
	for j := from + 2; j < len(r); j++ {
		if r[j] == '`' {
			return j
		}
	}
	return -1
}

// findPairClose finds a closing ** or __ pair with at least one content rune
// after from, matching parseInline's bold branch.
func findPairClose(r []rune, from int, mark rune) int {
	for j := from + 1; j+1 < len(r); j++ {
		if r[j] == mark && r[j+1] == mark {
			return j
		}
	}
	return -1
}

func trailingMarkerRun(r []rune) int {
	i := len(r)
	for i > 0 && isMarkdownMarker(r[i-1]) {
		i--
	}
	return i
}

// runeByteOffset converts a rune index to a byte index so cuts never split a
// UTF-8 sequence, including CJK and emoji.
func runeByteOffset(r []rune, idx int) int {
	return len(string(r[:idx]))
}
