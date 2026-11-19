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
	tail = trimPartialRune(text, tail)
	return text[:head] + "\n... [truncated] ...\n" + text[len(text)-tail:]
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
