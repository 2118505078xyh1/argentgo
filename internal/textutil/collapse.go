// Package textutil 的行折叠原语：当工具输出同时超过行数与字符数预算时，
// 保留尾部若干行（错误与堆栈常出现在最后几行）与头部少量上下文，中间用
// [N earlier lines] 标记被省略的行数。
//
// 它从 internal/tools/output_collapse.go 与 internal/shell 的镜像实现下沉而来：
// 两个包都要用它，若把它留在任一包都会因 tools import shell 形成 import 环。
// 放到零依赖的低层包后，tools 与 shell 都能安全 import，算法也只剩一个定义。
//
// 本原语是纯函数、纯标准库实现：不读写文件、不读取进程级配置、不改动入参、
// 不依赖调用顺序，因此可安全并发调用。FullPath 字段当前恒为空，它是预留给
// 未来接线点的位置——届时由落盘原语把全文写入磁盘后填入恢复路径；本原语
// 刻意不接收目录参数，也不做任何 I/O。
//
// 它不与 clipTo / clipBashOutput 共用代码，也不修改它们，因此现有输出帧
// 逐字节不变；是否接入由调用方决定。

package textutil

import (
	"strings"
	"unicode/utf8"
)

const (
	// defaultCollapseLines 是 maxLines<=0 时使用的默认行数预算。
	defaultCollapseLines = 200
	// defaultCollapseChars 是 maxChars<=0 时使用的默认字符（rune）预算。
	// 取值与 tools/shell 各自的 maxToolOutput 常量一致（40000），
	// 从而保证下沉前后两包的默认行为逐字节不变。
	defaultCollapseChars = 40000

	// 标记文本为 "[" + N + " earlier lines]"，全部为 ASCII。
	collapseMarkerHead = "["
	collapseMarkerTail = " earlier lines]"
)

// CollapseResult 是 CollapseOutput 的结果。
type CollapseResult struct {
	// Text 是折叠后的文本。未超限时与入参逐字节相同；超限时由头部少量上下文、
	// [N earlier lines] 标记与尾部行组成，且始终是合法 UTF-8。
	Text string
	// EarlyLines 是被标记省略的行数（即标记中的 N）。未超限时为 0。
	EarlyLines int
	// FullPath 预留给未来的全文落盘接线点；本纯函数恒返回空字符串。
	FullPath string
}

// CollapseOutput 按行数与字符数双重预算折叠 s。
//
// 语义：
//   - maxLines<=0 / maxChars<=0 分别用 defaultCollapseLines / defaultCollapseChars；
//   - "chars" 按 rune 计（CJK 与 emoji 各记 1），所有切点都落在 rune 边界，
//     绝不切坏 UTF-8；
//   - 未超限时原样返回（Text==s, EarlyLines==0, FullPath==""）；
//   - 超限时优先保留尾部行，其次保留头部少量上下文，中间插入
//     [N earlier lines]，N 为真正被省略的行数；
//   - 不改动入参、不产生 I/O、结果确定，可并发调用。
func CollapseOutput(s string, maxLines, maxChars int) CollapseResult {
	if maxLines <= 0 {
		maxLines = defaultCollapseLines
	}
	if maxChars <= 0 {
		maxChars = defaultCollapseChars
	}
	if s == "" {
		return CollapseResult{Text: s}
	}

	lines, trailingNL := collapseSplitLines(s)
	totalLines := len(lines)
	if totalLines == 0 {
		return CollapseResult{Text: s}
	}
	if totalLines <= maxLines && utf8.RuneCountInString(s) <= maxChars {
		return CollapseResult{Text: s}
	}

	// 标记独占一行：行预算减 1；字符预算为标记宽度与尾随换行做保守预留。
	// 用 totalLines 的位数计算预留，保证实际标记（位数<=totalLines）不会超支。
	lineBudget := maxLines - 1
	if lineBudget < 0 {
		lineBudget = 0
	}
	markerReserve := len(collapseMarkerHead) + collapseDigits(totalLines) + len(collapseMarkerTail)
	charBudget := maxChars - markerReserve
	if trailingNL {
		charBudget--
	}
	if charBudget < 0 {
		charBudget = 0
	}

	// 尾部约占行预算的 3/4，头部约占 1/4；字符预算由尾部优先使用。
	tailTarget := (lineBudget*3 + 3) / 4
	headTarget := lineBudget - tailTarget

	// 先挑尾部：每个被选行贡献"自身 rune 数 + 1 个行分隔符"。
	tailStart := totalLines
	tailCost := 0
	for totalLines-tailStart < tailTarget && tailStart > 0 {
		c := utf8.RuneCountInString(lines[tailStart-1]) + 1
		if tailCost+c > charBudget {
			break
		}
		tailCost += c
		tailStart--
	}
	tailCount := totalLines - tailStart

	// 极端情况：单行就超过字符预算时，仍保留最后一行末尾（错误常在行尾），
	// 做 rune 安全截断，避免输出只剩标记而丢失错误信息。
	if tailCount == 0 && lineBudget > 0 && charBudget > 1 {
		last := collapseKeepTailRunes(lines[totalLines-1], charBudget-1)
		lines[totalLines-1] = last
		tailStart = totalLines - 1
		tailCost = utf8.RuneCountInString(last) + 1
		tailCount = 1
	}

	// 再挑头部少量上下文：不与尾部重叠，只能用剩余字符预算。
	headEnd := 0
	headCost := 0
	for headEnd < headTarget && headEnd < tailStart {
		c := utf8.RuneCountInString(lines[headEnd]) + 1
		if headCost+tailCost+c > charBudget {
			break
		}
		headCost += c
		headEnd++
	}

	// 顶部补足：头部未占满的行槽位让给尾部，但 head+tail 不得超过行预算，
	// 且总字符数不得超过字符预算。
	for tailStart > headEnd &&
		headEnd+(totalLines-tailStart) < lineBudget &&
		headCost+tailCost+utf8.RuneCountInString(lines[tailStart-1])+1 <= charBudget {
		tailCost += utf8.RuneCountInString(lines[tailStart-1]) + 1
		tailStart--
	}
	tailCount = totalLines - tailStart

	earlier := totalLines - headEnd - tailCount
	if earlier < 0 {
		earlier = 0
	}

	var b strings.Builder
	for i := 0; i < headEnd; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(lines[i])
	}
	if headEnd > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(collapseMarkerHead)
	b.WriteString(collapseItoa(earlier))
	b.WriteString(collapseMarkerTail)
	for i := tailStart; i < totalLines; i++ {
		b.WriteByte('\n')
		b.WriteString(lines[i])
	}
	if trailingNL {
		b.WriteByte('\n')
	}

	return CollapseResult{Text: b.String(), EarlyLines: earlier}
}

// collapseSplitLines 按 '\n' 切分（'\n' 不会出现在多字节 rune 内部，故 rune 安全）。
// 返回每行的文本（不含分隔符）以及原串是否以换行结尾；末尾换行被视为终止符，
// 而非一个空行，与 read 工具的行号口径一致。
func collapseSplitLines(s string) (lines []string, trailingNL bool) {
	if s == "" {
		return nil, false
	}
	lines = strings.Split(s, "\n")
	if strings.HasSuffix(s, "\n") {
		trailingNL = true
		lines = lines[:len(lines)-1]
	}
	return lines, trailingNL
}

// collapseDigits 返回 n 的十进制位数；n<=0 记为 1 位。
func collapseDigits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		d++
		n /= 10
	}
	return d
}

// collapseItoa 把非负整数 n 转成十进制字符串，等价于 strconv.Itoa(n)。
// 这里手写是为了让本文件只依赖 strings 与 unicode/utf8，不引入额外 import。
func collapseItoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// collapseKeepTailRunes 取 s 末尾至多 n 个 rune（rune 安全，不切坏 UTF-8）。
func collapseKeepTailRunes(s string, n int) string {
	r := []rune(s)
	if n <= 0 {
		return ""
	}
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// ---------------------------------------------------------------------------
// 接线说明（设计上不在此实现 —— 本原语不与现有 clipBashOutput 相连）：
//
//   - 全文落盘与恢复路径属于外部 I/O，由调用方在拿到 CollapseOutput 的文本后，
//     复用落盘原语把原始全文写入磁盘，再把返回的 artifactPath 填入
//     CollapseResult.FullPath。本函数保持纯函数。
//   - 建议的接线位置是工具输出进入模型上下文之前（与 clip/clipTo 同层），
//     通过配置开关选择走旧的 clipTo 帧还是本原语，从而保证默认帧逐字节不变。
// ---------------------------------------------------------------------------
