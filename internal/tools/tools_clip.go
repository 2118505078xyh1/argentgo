package tools

import (
	"context"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
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
	// 头部只回退到最近的字素簇边界，避免切断组合序列/ZWJ emoji/
	// 旗帜等；textutil.ClusterBoundaryBefore 内部复用 TrimPartialRune，
	// 因此仍保证 rune 对齐。
	head = textutil.ClusterBoundaryBefore(text, head)
	// 尾部切片从 len(text)-tail 开始，而不是从 tail 开始。用
	// TrimPartialRune 回退这个起点会检查错误的字节，并可能让
	// 后缀从 rune 中间开始；应把它向前移到下一个
	// rune 边界，使保留的后缀始终是有效 UTF-8。
	tailStart := len(text) - tail
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	// 尾部只前进到最近的字素簇边界，确保后缀不会以悬空的
	// 组合标记/ZWJ 开头；与头部一样，保留字节只减不增。
	tailStart = textutil.ClusterBoundaryAfter(text, tailStart)
	return text[:head] + "\n... [truncated] ...\n" + text[tailStart:]
}

// trimPartialRune 是包内别名，保留给 web_fetch/web_search/output_spill
// 等既有调用方；真正的实现已下沉到 textutil.TrimPartialRune，消除重复。
func trimPartialRune(text string, offset int) int {
	return textutil.TrimPartialRune(text, offset)
}
