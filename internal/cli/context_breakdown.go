package cli

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 上下文分解视图的四个固定分类名称。顺序即为展示顺序：
// 系统提示、工具定义、历史消息、文件内容。
const (
	contextSegSystem  = "系统提示"
	contextSegSchema  = "工具定义"
	contextSegHistory = "历史消息"
	contextSegFiles   = "文件内容"
)

// ContextSegment 是上下文占用分解中的一类。
type ContextSegment struct {
	Name    string
	Tokens  int
	Percent float64
}

// BreakdownContext 把四类 token 占用折算为带百分比的分解视图。
//
// 它是纯函数：只读四个整数参数，不读取配置、会话或全局状态，也不产生任何
// I/O。分母恒为四类占用之和；总和为 0（即四类全为 0）时所有百分比为 0，
// 避免除零。返回值按「系统提示、工具定义、历史消息、文件内容」的固定顺序
// 排列，调用方传入的 token 数不会被修改（整数按值传递）。
//
// 本函数不做任何文本切分，因此对 CJK/emoji 安全；token 的 CJK 折算由调用方
// 在传入前用 tokenize.Estimate 完成。当前尚未接线到任何命令，供后续 opt-in
// 的上下文分解展示使用。
func BreakdownContext(systemTokens, schemaTokens, historyTokens, filesTokens int) []ContextSegment {
	values := [...]int{systemTokens, schemaTokens, historyTokens, filesTokens}
	names := [...]string{contextSegSystem, contextSegSchema, contextSegHistory, contextSegFiles}

	total := 0
	for _, v := range values {
		total += v
	}

	segs := make([]ContextSegment, 0, len(values))
	for i, v := range values {
		percent := 0.0
		if total != 0 {
			percent = float64(v) / float64(total) * 100
		}
		segs = append(segs, ContextSegment{Name: names[i], Tokens: v, Percent: percent})
	}
	return segs
}

// FormatContextBreakdown 把分解视图渲染为稳定的中文表格文本。
//
// 输出按给定顺序逐行排列，百分比保留一位小数。列宽按「显示宽度」对齐：
// CJK/全角字符占两列，ASCII 占一列，因此中文分类名不会破坏表格对齐。
// 函数只读 segs，不修改其元素，也不对切片排序。
func FormatContextBreakdown(segs []ContextSegment) string {
	var b strings.Builder
	b.WriteString("上下文占用分解\n")
	if len(segs) == 0 {
		b.WriteString("（无数据）\n")
		return b.String()
	}

	pcts := make([]string, len(segs))
	nameW := displayWidth("分类")
	tokenW := len("Tokens")
	pctW := displayWidth("占比")
	for i, s := range segs {
		if w := displayWidth(s.Name); w > nameW {
			nameW = w
		}
		if w := len(strconv.Itoa(s.Tokens)); w > tokenW {
			tokenW = w
		}
		pcts[i] = fmt.Sprintf("%.1f%%", s.Percent)
		if w := displayWidth(pcts[i]); w > pctW {
			pctW = w
		}
	}

	b.WriteString(padRight("分类", nameW) + "  " + padLeft("Tokens", tokenW) + "  " + padLeft("占比", pctW) + "\n")
	for i, s := range segs {
		b.WriteString(padRight(s.Name, nameW) + "  " +
			padLeft(strconv.Itoa(s.Tokens), tokenW) + "  " +
			padLeft(pcts[i], pctW) + "\n")
	}
	return b.String()
}

// ContextHealth 根据分解视图与上下文窗口大小生成一条稳定的中文健康提示。
//
// window <= 0 时信息不足，返回固定文案。占用百分比按 segs 的 token 总和除以
// window 计算并按四舍五入取整，分档为：充裕（<60%）、偏高（60%~90%）、
// 接近上限（90%~100%）、超出上限（>100%）。函数只读 segs，不修改其元素，
// 也不会把任何原始文本内容嵌入返回值。
func ContextHealth(segs []ContextSegment, window int) string {
	if window <= 0 {
		return "上下文窗口未知，无法评估占用"
	}
	used := 0
	for _, s := range segs {
		used += s.Tokens
	}
	pct := int(math.Round(float64(used) / float64(window) * 100))

	prefix := "上下文占用 " + strconv.Itoa(pct) + "%"
	var health string
	switch {
	case pct > 100:
		health = prefix + "，已超出窗口上限"
	case pct >= 90:
		health = prefix + "，已接近上限"
	case pct >= 60:
		health = prefix + "，占用偏高"
	default:
		health = prefix + "，窗口充裕"
	}
	// 接近或超出上限时附上可执行的缩减建议（此前 contextBudgetAdvice 死代码）。
	// 低于阈值时 advice 为空，输出与接线前逐字节一致。
	if advice := contextBudgetAdvice(used, window); advice != "" {
		health += "；" + advice
	}
	return health
}

// displayWidth 返回 s 在等宽终端上占用的列数：全角（CJK/emoji）字符占两列，
// 其余字符占一列。非法 UTF-8 字节按单列处理，保证任意输入都得到确定的结果。
func displayWidth(s string) int {
	w := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			w++
		} else if isFullWidth(r) {
			w += 2
		} else {
			w++
		}
		s = s[size:]
	}
	return w
}

// isFullWidth 报告 r 是否属于东亚宽字符（含汉字、假名、韩文、全角形式、
// CJK 标点与常见 emoji），这些字符在等宽终端上占两列。
func isFullWidth(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || // 韩文字母
		(r >= 0x2E80 && r <= 0xA4CF) || // CJK 部首、康熙字典、汉字
		(r >= 0xAC00 && r <= 0xD7A3) || // 韩文音节
		(r >= 0xF900 && r <= 0xFAFF) || // CJK 兼容汉字
		(r >= 0xFE10 && r <= 0xFE19) || // CJK 标点
		(r >= 0xFE30 && r <= 0xFE6F) || // CJK 兼容形式
		(r >= 0xFF00 && r <= 0xFF60) || // 全角形式
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1FAFF) || // 符号与 emoji
		(r >= 0x20000 && r <= 0x3FFFD) // CJK 扩展平面
}

// padRight 在 s 右侧补空格，使其显示宽度达到 width；已超出时不改动。
func padRight(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// padLeft 在 s 左侧补空格，使其显示宽度达到 width；已超出时不改动。
func padLeft(s string, width int) string {
	if n := width - displayWidth(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}
