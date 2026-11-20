package tools

import (
	"context"
	"unicode/utf8"
)

func clip(ctx context.Context, text string) string {
	// 仅限 opt-in：当 ctx 中没有溢出配置（也没有 env 开关）时，
	// 这正是旧的 clipTo 路径，因此所有现有调用方都不受影响。
	if out, handled := spillToolOutput(ctx, text); handled {
		return out
	}
	return clipTo(text, outputLimit(ctx))
}

// clipTo 保留过大输出的头部和尾部。
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
	// 尾部切片从 len(text)-tail 开始，而不是从 tail 开始。用
	// trimPartialRune 回退这个起点会检查错误的字节，并可能让
	// 后缀从 rune 中间开始；应把它向前移到下一个
	// rune 边界，使保留的后缀始终是有效 UTF-8。
	tailStart := len(text) - tail
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	return text[:head] + "\n... [truncated] ...\n" + text[tailStart:]
}

// trimPartialRune 把字节偏移从 UTF-8 序列的中间回退。
func trimPartialRune(text string, offset int) int {
	if offset <= 0 {
		return 0
	}
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}
