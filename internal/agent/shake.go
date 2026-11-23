package agent

import (
	"os"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tokenize"
)

const (
	// shakeProtectRunes 是最新上下文的保护窗口：从消息列表尾部向前累计这么多个 rune，
	// 窗口内的文本一律不剥离，避免模型正在使用的代码/文档被抽走。
	shakeProtectRunes = 4096
	// shakeMinRunes 是单个块的最小收益。只有 rune 数达到它的 fenced/XML 块才值得替换，
	// 短块保持原样，使默认路径上的短对话逐字节不变。
	shakeMinRunes = 512
	// shakePlaceholderFmt 是剥离后留下的占位行。它保留"这里曾有一大段文本"的语义，
	// 使模型知道内容被机械回收而不是从未产生。
	shakePlaceholderFmt = "[shaken: %d chars of a large block removed to save context]"
)

// shakeOff 报告是否通过环境变量禁用剥离。新功能默认开启，
// 环境变量只作为 =0 的回退（零用户选择），其余值一律保持开启。
func shakeOff() bool {
	return featureDisabled("ARGENT_SHAKE")
}

// featureDisabled 实现"默认开启、=0 回退"的统一语义。
func featureDisabled(env string) bool {
	return strings.TrimSpace(os.Getenv(env)) == "0"
}

// blockRange 描述文本中一个可剥离的连续行区间（含首尾行号）。
type blockRange struct {
	start, end int
	runes      int
}

// scanTextForBlockRanges 是纯 stdlib 的逐行状态机，找出：
//   - ``` 或 ~~~ 围栏块（开闭符必须同字符且闭符长度不小于开符）；
//   - 顶层成对 XML 标签块（围栏内抑制 XML，未闭合不出范围）。
//
// 返回区间按 start 升序且互不重叠。按 rune 计数使 CJK/emoji 不会被切坏。
func scanTextForBlockRanges(text string, minRunes int) []blockRange {
	lines := strings.Split(text, "\n")
	var out []blockRange
	fenceChar := byte(0)
	fenceLen := 0
	fenceStart := 0
	xmlTag := ""
	xmlStart := 0

	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if fenceChar != 0 {
			// 围栏内：只寻找闭符，抑制 XML 识别。
			if n := fenceRun(trimmed, fenceChar); n >= fenceLen {
				r := utf8.RuneCountInString(strings.Join(lines[fenceStart:i+1], "\n"))
				if r >= minRunes {
					out = append(out, blockRange{start: fenceStart, end: i, runes: r})
				}
				fenceChar, fenceLen = 0, 0
			}
			continue
		}
		if ch, n, ok := fenceOpen(trimmed); ok {
			// 未闭合的围栏不出范围。
			fenceChar, fenceLen, fenceStart = ch, n, i
			continue
		}
		if xmlTag == "" {
			if tag, ok := topLevelXMLOpen(line); ok {
				xmlTag, xmlStart = tag, i
			}
			continue
		}
		if topLevelXMLClose(line, xmlTag) {
			r := utf8.RuneCountInString(strings.Join(lines[xmlStart:i+1], "\n"))
			if r >= minRunes {
				out = append(out, blockRange{start: xmlStart, end: i, runes: r})
			}
			xmlTag = ""
		}
	}
	return out
}

// fenceOpen 识别围栏开行，返回围栏字符与长度。前置空白不超过 3 个空格，
// 且开行不得带闭符语义（闭符由 fenceRun 单独判断）。
func fenceOpen(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 {
		return 0, 0, false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	return ch, n, true
}

// fenceRun 返回以 ch 组成的连续前缀长度；非 ch 开头时返回 0。
func fenceRun(trimmed string, ch byte) int {
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	return n
}

// topLevelXMLOpen 识别顶层的成对 XML 开标签。要求标签从第 0 列开始，
// 避免把缩进的代码片段误判为 XML。返回标签名。
func topLevelXMLOpen(line string) (string, bool) {
	if line == "" || line[0] != '<' || strings.HasPrefix(line, "</") ||
		strings.HasPrefix(line, "<?") || strings.HasPrefix(line, "<!") {
		return "", false
	}
	end := strings.IndexAny(line, " />\t")
	if end <= 1 {
		return "", false
	}
	tag := line[1:end]
	if !validXMLTagName(tag) {
		return "", false
	}
	// 同一行闭合的标签不构成"大块"，无需进入状态。
	if strings.Contains(line, "</"+tag+">") {
		return "", false
	}
	return tag, true
}

// topLevelXMLClose 报告 line 是否为 tag 的顶层闭标签。
func topLevelXMLClose(line string, tag string) bool {
	if !strings.HasPrefix(line, "</") {
		return false
	}
	rest := line[2:]
	end := strings.IndexAny(rest, " >\t")
	if end <= 0 {
		return false
	}
	return rest[:end] == tag
}

// validXMLTagName 只接受字母/数字/下划线/连字符/冒号/点，避免把比较表达式
// （如 a < b）误判为 XML。
func validXMLTagName(tag string) bool {
	if tag == "" {
		return false
	}
	for i := 0; i < len(tag); i++ {
		c := tag[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '-' || c == ':' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// replaceBlockRanges 按区间把文本替换为占位行。区间按 start 升序且不重叠，
// 从后往前替换使前面的行号保持有效。返回新文本与净节省的估算 token。
func replaceBlockRanges(text string, ranges []blockRange) (string, int) {
	lines := strings.Split(text, "\n")
	freed := 0
	for i := len(ranges) - 1; i >= 0; i-- {
		r := ranges[i]
		if r.start < 0 || r.end >= len(lines) || r.start > r.end {
			continue
		}
		removed := strings.Join(lines[r.start:r.end+1], "\n")
		placeholder := strings.ReplaceAll(shakePlaceholderFmt, "%d", itoaRuneCount(utf8.RuneCountInString(removed)))
		delta := tokenize.Estimate(removed) - tokenize.Estimate(placeholder)
		if delta <= 0 {
			continue
		}
		lines = append(append(append([]string{}, lines[:r.start]...), placeholder), lines[r.end+1:]...)
		freed += delta
	}
	return strings.Join(lines, "\n"), freed
}

// itoaRuneCount 把 rune 数转为十进制字符串，避免为此引入 strconv 之外的依赖。
func itoaRuneCount(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// ShakeBlocks 从消息列表中机械剥离大段 fenced 代码块与顶层 XML 块。
//
// 设计要点（对应 oh-my-pi 的 shake）：
//   - 只改写 assistant/user 的纯文本 Content；带图消息（ContentParts）、工具调用、
//     工具结果、系统消息与受保护窗口内的消息原样保留，工具调用/结果配对不被拆散；
//   - protectRunes 是从尾部向前累计的保护窗口，minRunes 是单块最小收益，均为 rune 计数；
//   - 返回被剥离文本的净估算 token（elidedOutputTokens），使调用方能在重放计数中
//     扣除这部分已不再出现在上下文里的输出。
//
// 本函数是纯函数：不读写全局状态（开关由调用方判断）、不修改入参切片与消息内容。
func ShakeBlocks(messages []llm.Message, protectRunes, minRunes int) ([]llm.Message, int) {
	if len(messages) == 0 {
		return messages, 0
	}
	if protectRunes <= 0 {
		protectRunes = shakeProtectRunes
	}
	if minRunes <= 0 {
		minRunes = shakeMinRunes
	}

	// 从尾部累计受保护的 rune，直到覆盖 protectRunes；再往前的消息才可剥离。
	protected := make([]bool, len(messages))
	budget := protectRunes
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role == "system" || m.Role == "tool" {
			protected[i] = true
			continue
		}
		protected[i] = budget > 0
		budget -= utf8.RuneCountInString(m.Content)
	}

	out := make([]llm.Message, len(messages))
	copy(out, messages)
	elided := 0
	for i, m := range out {
		if protected[i] || m.Content == "" || len(m.ToolCalls) > 0 || len(m.ContentParts) > 0 {
			continue
		}
		ranges := scanTextForBlockRanges(m.Content, minRunes)
		if len(ranges) == 0 {
			continue
		}
		text, freed := replaceBlockRanges(m.Content, ranges)
		if freed <= 0 || text == m.Content {
			continue
		}
		out[i].Content = text
		elided += freed
	}
	return out, elided
}
