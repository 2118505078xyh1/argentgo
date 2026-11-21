package tui

import (
	"fmt"
	"strings"
	"time"
)

// reasoningTailDefault 是尾窗默认保留的行数： folding 时只显示
// 推理的最后若干行，更早的内容由首行的折叠提示概括。
const reasoningTailDefault = 6

// ReasoningTailWindow 是推理尾窗渲染的 opt-in 开关，默认关闭，
// 使现有的单行摘要路径与字节级渲染测试保持逐字一致。打开后，
// reasoningLines 会在摘要之下追加折叠提示与尾部若干行。把它保留为
// 普通包变量，方便以后接到 CLI 标志或配置而无需改动渲染器。
var ReasoningTailWindow = false

// reasoningHiddenHint 返回折叠提示的格式串，其中 %d 会被替换为
// 被折叠的行数。措辞按 locale 选择，保持与其他 UI 文案一致。
func reasoningHiddenHint(locale string) string {
	if strings.HasPrefix(locale, "zh") {
		return "… %d 行已折叠"
	}
	return "… %d lines folded"
}

// reasoningTailWindow 返回推理行的尾窗：当行数超过 tail 时，
// 只用一行 hiddenHint 提示概括被折叠的行数，随后保留最后 tail 行；
// 行数不超过 tail（或 tail <= 0）时原样返回内容的副本。
//
// hiddenHint 必须是含一个 %d 动词的格式串，%d 会被替换为被折叠的
// 行数，例如 "… %d 行已折叠"。该函数是纯函数：它按行切片，从不
// 按字节切割字符串，因此汉字、emoji 与其他多字节 rune 始终完整
// （CJK 安全）。返回副本使调用方改动不会污染输入，且行数不超过
// 预算时与输入逐字一致。
func reasoningTailWindow(lines []string, tail int, hiddenHint string) []string {
	if tail <= 0 || len(lines) <= tail {
		return append([]string(nil), lines...)
	}
	hidden := len(lines) - tail
	out := make([]string, 0, tail+1)
	out = append(out, fmt.Sprintf(hiddenHint, hidden))
	out = append(out, lines[len(lines)-tail:]...)
	return out
}

// reasoningWindowedLines 在 ReasoningTailWindow 打开时渲染推理块：
// 首行仍是原有的单行摘要（含 spinner / 时长），其下按尾窗追加
// 折叠提示与尾部推理行。每行以两个空格缩进并使用较暗样式，
// 与工具输出预览的层级保持一致。
func (m *Model) reasoningWindowedLines(entry Entry, width int, elapsed time.Duration, done bool) []Line {
	out := []Line{reasoningSummaryLine(entry.Text, elapsed, width, done, m.locale, m.palette)}
	folded := reasoningTailWindow(splitResultLines(entry.Text), reasoningTailDefault, reasoningHiddenHint(m.locale))
	for _, text := range folded {
		line := NewLine()
		line.Push("  ", Style{})
		line.Push(Clip(text, maxInt(width-2, 0)), m.palette.dim())
		out = append(out, line)
	}
	return out
}

// reasoningExpandedLines 在思考块被点击展开后渲染：首行仍是原有的
// 单行摘要（含 spinner / 时长），其下逐行显示完整思考。每行按字素簇
// Clip 到宽度并缩进两格，因此 CJK / emoji 不会被切开，也不会撑破布局。
// 折叠态完全不走这条路径，默认帧保持逐字节不变。
func (m *Model) reasoningExpandedLines(entry Entry, width int, elapsed time.Duration, done bool) []Line {
	out := []Line{reasoningSummaryLine(entry.Text, elapsed, width, done, m.locale, m.palette)}
	for _, raw := range splitResultLines(entry.Text) {
		line := NewLine()
		line.Push("  ", Style{})
		line.Push(Clip(raw, maxInt(width-2, 0)), m.palette.dim())
		out = append(out, clipLine(line, maxInt(width, 1)))
	}
	return out
}
