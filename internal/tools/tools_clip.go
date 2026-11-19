package tools

import (
	"context"
	"unicode/utf8"
)

func clip(ctx context.Context, text string) string {
	return clipTo(text, outputLimit(ctx))
}

// clipTo keeps the head and the tail of an oversized output.
func clipTo(text string, limit int) string {
	if limit <= 0 {
		limit = maxToolOutput
	}
	if len(text) <= limit {
		return text
	}
	head := limit * 2 / 3
	tail := limit - head
	head = trimPartialRune(text, head)
	// The tail slice begins at len(text)-tail, not at tail. Backing that start
	// off with trimPartialRune would check the wrong byte and could leave the
	// suffix starting in the middle of a rune; move it forward to the next
	// rune boundary instead so the kept suffix is always valid UTF-8.
	tailStart := len(text) - tail
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	return text[:head] + "\n... [truncated] ...\n" + text[tailStart:]
}

// trimPartialRune backs a byte offset off the middle of a UTF-8 sequence.
func trimPartialRune(text string, offset int) int {
	if offset <= 0 {
		return 0
	}
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}
