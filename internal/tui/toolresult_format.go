package tui

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// StructuredToolResults 让工具结果预览选择结构化渲染器：
// JSON 对象/数组被漂亮打印，统一差异保留其框架，
// 纯文本回退到普通的行切分（见 formatToolResultContent）。
//
// 它默认关闭。toolLines 中的原始预览渲染保留为
// 规范的、逐字节一致的路径，使现有布局和基准测试
// 不发生变化；打开此开关可预览结构化路径。把开关
// 保留为普通包变量，使以后的更改可把它接到 CLI 标志/配置
// 而无需触碰渲染器。
var StructuredToolResults = false

// structuredOutputLines 通过 formatToolResultContent 渲染工具结果。
// 该格式化器已经限制行数并追加自己的 "… N more lines"
// 标记，因此该辅助函数只应用视觉前缀和样式，绝不
// 添加第二个溢出标记。标记行使用较暗的样式以匹配
// 原始预览的溢出行。
func structuredOutputLines(raw string, limit, width int, p Palette) []Line {
	bodyWidth := maxInt(width-2, 1)
	// 只有启用 StructuredToolResults 时才会到达此渲染器。
	// toolLines 中逐字节一致的旧预览保持不变；在这里清洗
	// 可防止进度条光标转义进入帧缓存以及
	// 工具结果之后绘制的行。
	textLines := formatToolResultContent(normalizeToolOutput(raw), limit)
	out := make([]Line, 0, len(textLines))
	for _, text := range textLines {
		line := NewLine()
		line.Push("  ", Style{})
		if strings.HasPrefix(text, "… ") && strings.HasSuffix(text, " more lines") {
			line.Push(text, p.dim())
		} else {
			line.Push(Clip(text, bodyWidth), p.muted())
		}
		out = append(out, line)
	}
	return out
}

// formatToolResultContent 把原始工具结果字符串转换为展示行。
// 它是纯格式化器，不依赖渲染器：MCP 工具常发出的结构化载荷
// （漂亮打印的 JSON、统一差异）被保留为
// 可读的逐行文本，而不是扁平化为一个不可读的块。
// 纯文本回退到普通行切分。
//
// 输出被限制在 maxLines 行（当内容被丢弃时最后一位成为
// "… N more lines" 标记），且绝不拆分 UTF-8 rune：每次
// 断行都发生在 ASCII '\n' 字节上。不引入新依赖。
func formatToolResultContent(raw string, maxLines int) []string {
	if maxLines <= 0 {
		return nil
	}

	var lines []string
	switch {
	case isJSONObjectOrArray(raw):
		lines = prettyJSONLines(raw)
	case looksLikeUnifiedDiff(raw):
		lines = splitResultLines(raw)
	default:
		lines = splitResultLines(raw)
	}
	return capResultLines(lines, maxLines)
}

// isJSONObjectOrArray 报告 raw 是否为顶层值为
// 对象或数组的有效 JSON。标量（字符串、数字、true/false、null）被
// 有意排除：缩进它们不会增加可读性，而一个裸词
// 应保留纯文本路径。
func isJSONObjectOrArray(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return false
	}
	return json.Valid([]byte(trimmed))
}

// prettyJSONLines 以两空格步长缩进已验证的 JSON 对象/数组，
// 并为每个输出行返回一个字符串。对 UTF-8 字符串值，缩进保持字节不变，
// 因此 JSON 中的 CJK 和 emoji 原样保留。
func prettyJSONLines(raw string) []string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(strings.TrimSpace(raw)), "", "  "); err != nil {
		// 防御性：json.Valid 已通过，但绝不破坏载荷。
		return splitResultLines(raw)
	}
	return splitResultLines(buf.String())
}

// looksLikeUnifiedDiff 识别标准的统一差异框架。一个块
// 头（"@@ ..."）本身就是强信号；否则必须同时存在两个文件
// 头（"--- " 和 "+++ "）。要求成对可避免
// 把单独一行 markdown 水平线（"---"）误认为差异。
func looksLikeUnifiedDiff(raw string) bool {
	hasMinus, hasPlus, hasHunk := false, false, false
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "@@ "):
			hasHunk = true
		case strings.HasPrefix(line, "--- "):
			hasMinus = true
		case strings.HasPrefix(line, "+++ "):
			hasPlus = true
		}
		if hasHunk || (hasMinus && hasPlus) {
			return true
		}
	}
	return false
}

// splitResultLines 按 '\n' 把 raw 切分为逻辑行，规范化 CRLF 并
// 丢弃尾随换行本会产生的空哨兵。切分
// 在 ASCII 字节上切分使每个多字节 rune 和 ANSI 转义序列保持完整。
func splitResultLines(raw string) []string {
	if raw == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(raw, "\n")
	trimmed = strings.TrimSuffix(trimmed, "\r")
	parts := strings.Split(trimmed, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, strings.TrimSuffix(part, "\r"))
	}
	return lines
}

// capResultLines 最多保留 maxLines 行。当必须丢弃内容时，
// "… N more lines" 标记占据最后一个槽位（与渲染器自身的
// 预览约定一致），使返回的切片永不超出预算。
func capResultLines(lines []string, maxLines int) []string {
	if len(lines) <= maxLines {
		return lines
	}
	if maxLines <= 0 {
		return nil
	}
	kept := append([]string(nil), lines[:maxLines]...)
	if maxLines > 1 {
		kept[maxLines-1] = "… " + strconv.Itoa(len(lines)-maxLines+1) + " more lines"
	}
	return kept
}
