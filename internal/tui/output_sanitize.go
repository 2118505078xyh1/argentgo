package tui

import "strings"

// 本文件防止原始工具输出破坏 TUI 画面。cargo、npm
// 和 git 等程序用 CSI 光标/擦除序列和裸回车
// 来动画化进度条。当那些字节被复制到记录
// Line 时，它们会原样到达 Buffer：帧缓存每个进度 tick 都看到
// 一个"不同的"行，杂散转义被重放到终端，
// 使之后绘制的每一行光标移位。
//
// 这里的辅助函数是纯字符串变换，不依赖渲染器。公共入口
// normalizeToolOutput 有意没有接入 toolLines 的默认预览路径：该路径
// 对现有布局/基准测试保持逐字节一致。它只由 opt-in 的结构化
// 渲染器（StructuredToolResults）应用；
// 见 structuredOutputLines。

// stripCursorControl 移除移动光标、擦除
// 屏幕一部分或切换终端模式的 ANSI 转义序列。它仿照 Crush 的
// StripCursorControl，仅使用标准库：
//
//   - 保留：SGR 颜色/样式（CSI 终结字节 'm'）、OSC 序列，如
//     OSC 8 超链接、字符集指定符和未知转义；
//   - 移除：CSI 光标移动（A-H、f）、擦除（J、K）、滚动（S、T）、
//     保存/恢复（s、u）和模式设置/复位（h、l，包括 DEC '?' 形式），
//     以及两字节的 ESC 7 / ESC 8 保存/恢复光标序列。
//
// 被截断的序列绝不会被丢弃：保持其完整比猜测其
// 结束位置更安全。没有 ESC 字节的输入被原样返回。
func stripCursorControl(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			out.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) { // bare ESC at EOF: no sequence to classify
			out.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case '[': // CSI: parameter/intermediate bytes, final byte 0x40..0x7e.
			end := scanCSISequence(s, i+2)
			seq := s[i:end]
			if isCursorControlCSI(seq) {
				i = end
				continue
			}
			out.WriteString(seq)
			i = end
		case ']': // OSC: keep the whole payload (BEL- or ST-terminated).
			end := scanOSCSequence(s, i+2)
			out.WriteString(s[i:end])
			i = end
		case '7', '8': // DEC save/restore cursor.
			i += 2
		default:
			// 其他两字节转义（字符集指定符、ESC = 等）。
			// 更长的转义只是把其尾部留给下一次迭代。
			out.WriteString(s[i : i+2])
			i += 2
		}
	}
	return out.String()
}

// scanCSISequence 返回从 i（紧跟 ESC '[' 的字节）开始、
// 越过下一个 CSI 终结字节之后的索引。如果输入先结束，它返回
// len(s)，使被截断的序列完整保留。
func scanCSISequence(s string, i int) int {
	for i < len(s) {
		b := s[i]
		i++
		if b >= 0x40 && b <= 0x7e {
			return i
		}
	}
	return i
}

// scanOSCSequence 返回从 i（紧跟 ESC ']' 的字节）开始、
// 越过 OSC 终结符（BEL 0x07 或 ST ESC '\'）之后的索引。未终结的
// OSC 被消费到末尾，使部分载荷不泄漏进画面。
func scanOSCSequence(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == 0x07:
			return i + 1
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		}
		i++
	}
	return len(s)
}

// isCursorControlCSI 报告 seq 是否为终结字节
// 要求终端移动或擦除某物的 CSI 序列。'm'（SGR）和每个未知
// 终结字节都被有意排除，使颜色和诊断信息得以保留。
func isCursorControlCSI(seq string) bool {
	if len(seq) < 3 { // need at least ESC '[' and a final byte
		return false
	}
	switch seq[len(seq)-1] {
	case 'A', 'B', 'C', 'D', // cursor up / down / forward / backward
		'E', 'F', // cursor to start/end of next/previous line
		'G',      // cursor horizontal absolute
		'H', 'f', // cursor position
		'J',      // erase in display
		'K',      // erase in line
		'S', 'T', // scroll up / down
		's', 'u', // save / restore cursor
		'h', 'l': // set / reset mode (including DEC private '?' forms)
		return true
	}
	return false
}

// simulateCarriageReturns 应用 Crush 的 StripCursorControl
// 所用的"最后一帧胜出"规则：在每行内，最后一个 '\r' 之后的文本
// 是用户将看到的内容，因此更早的进度帧被丢弃。先按 '\n'
// 切分可保持行边界（和尾随换行）不变。没有
// '\r' 的输入被原样返回。
func simulateCarriageReturns(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.LastIndexByte(line, '\r'); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	return strings.Join(lines, "\n")
}

// normalizeOutputSpace 执行终端对捕获输出施加的两种空白
// 重写：CRLF 变为 LF，每个制表符变为四个空格。
// 前导制表符就地展开，因此缩进深度得以保留。
// 裸 '\r' 留给 simulateCarriageReturns 处理。既不含
// '\r' 也不含 '\t' 的输入被逐字节原样返回。
func normalizeOutputSpace(s string) string {
	if !strings.ContainsAny(s, "\r\t") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	return strings.ReplaceAll(s, "\t", "    ")
}

// normalizeToolOutput 按终端顺序运行三遍：先展开
// 空白，使 CRLF 不被误认为裸覆盖，然后丢弃
// 光标控制字节，最后处理剩余的回车。SGR
// 颜色和 OSC 超链接保留，CJK 和 emoji rune 也保留。
func normalizeToolOutput(s string) string {
	return simulateCarriageReturns(stripCursorControl(normalizeOutputSpace(s)))
}
