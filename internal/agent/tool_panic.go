package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// toolPanicMaxValueBytes 限制一条 panic 值文本的最大字节数。
	// 一次失控的 panic（例如把超大缓冲区或深结构体作为值抛出）不应污染模型上下文；
	// 这里取 512 字节，足以呈现诊断信息，又不会占用过多上下文预算。
	toolPanicMaxValueBytes = 512

	// toolPanicTruncatedMarker 在值文本被裁剪时追加在末尾，
	// 让模型明确知道它看到的只是原始 panic 值的前缀。
	toolPanicTruncatedMarker = "…(truncated)"
)

// safeToolPanic 把 recover() 捕获的 panic 值规范化为一条模型可读的错误。
// 工具 worker 在 defer 中调用它，即可把单个 panic 隔离成普通的工具失败结果，
// 而不是让异常逃逸、拖垮整个运行中的进程。
//
// 该函数是纯函数：无 I/O、不访问时钟与共享状态、不修改任何输入，
// 相同参数总是逐字节返回相同的结果，因此可在任意 worker 的 defer 中安全调用。
//
// 行为约定：
//   - p == nil（未发生 panic）时返回 nil，调用方据此区分"无需处理"与"确实发生了 panic"。
//     注意现代 Go 中 panic(nil) 会让 recover() 返回 *runtime.PanicNilError 而非 nil，
//     该情形会被如实地渲染成错误，不影响本函数对真正 nil 输入的约定。
//   - 错误文本形如 "tool panic (<类型>): <值>"。<类型> 取自 %T，
//     使模型能区分字符串 panic、错误对象 panic 与其它任意值；<值> 是 fmt.Sprint
//     得到的文本表示（fmt 会吞掉格式化方法里的二次 panic，避免辅助函数自身崩溃）。
//   - 值文本里的控制字符（换行、制表、DEL 等）被逐 rune 替换为空格，避免破坏单行
//     错误布局；随后按 rune 边界有界裁剪，CJK 等多字节字符不会被切断，
//     结果始终是合法 UTF-8。类型名由编译期决定，本身有界，故整条错误亦保持有界。
func safeToolPanic(p any) error {
	if p == nil {
		return nil
	}
	kind := fmt.Sprintf("%T", p)
	value := toolPanicValueText(p)
	return fmt.Errorf("tool panic (%s): %s", kind, clipToolPanicText(value, toolPanicMaxValueBytes))
}

// toolPanicValueText 提取 panic 值的可读文本：统一转成字符串（fmt 对 Stringer/Error
// 方法里的再次 panic 有兜底），把控制字符替换为空格以保持单行布局；空值则给出占位符，
// 保证返回值非空且可读。strings.Map 逐 rune 处理，天然不会切断多字节字符。
func toolPanicValueText(p any) string {
	text := fmt.Sprint(p)
	if text == "" {
		text = "(empty)"
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, text)
}

// clipToolPanicText 把 text 裁剪到不超过 maxBytes 字节，只保留开头，
// 截断时在末尾追加 toolPanicTruncatedMarker。切点回退到 rune 边界，
// 因此即便切点落在多字节 rune 内部，结果也始终是合法 UTF-8（CJK 安全）。
func clipToolPanicText(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	limit := maxBytes - len(toolPanicTruncatedMarker)
	marker := toolPanicTruncatedMarker
	if limit <= 0 {
		// 预算连标记都放不下：放弃标记，只保留有界前缀。
		limit = maxBytes
		marker = ""
	}
	// 将切点回退到下一个 rune 起点，避免把一个多字节字符切成两半。
	for limit > 0 && limit < len(text) && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + marker
}
