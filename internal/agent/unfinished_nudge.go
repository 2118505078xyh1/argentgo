package agent

import (
	"strings"
	"unicode/utf8"
)

// unfinishedReminder 是一次性续做提醒。只在最后一次回复没有工具调用、
// 且以冒号/意图句收尾时注入，避免模型说完"正在排查："就结束轮次。
const unfinishedReminder = "你的上一条回复似乎还没做完（以冒号或承诺下一步的句子收尾）。请继续使用工具完成刚才承诺的步骤，或直接给出完整答案；若确实已完成，请明确说明。"

// unfinishedIntentMarkers 是"承诺了下一步却没做"的短句前缀。
// 问句不触发：调用方会先排除以 ?/？ 结尾的回复。
var unfinishedIntentMarkers = []string{
	"我先查", "我先检查", "我来查", "我来检查", "我接下来", "接下来我",
	"接下来我将", "下面我", "下面我将", "让我检查", "让我看看", "我将",
	"我准备", "正在检查", "正在排查", "正在分析",
}

// unfinishedOff 报告是否通过环境变量禁用续做提醒（默认开启）。
func unfinishedOff() bool { return featureDisabled("ARGENT_UNFINISHED_NUDGE") }

// lastVisibleLine 返回最后一个非空行（去掉首尾空白）。
// 忽略 fenced 代码块内的行，避免把代码块里的冒号误判成"没说完"。
func lastVisibleLine(text string) string {
	lines := strings.Split(text, "\n")
	fence := byte(0)
	last := ""
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if fence != 0 {
			if n := fenceRun(trimmed, fence); n >= 3 {
				fence = 0
			}
			continue
		}
		if ch, n, ok := fenceOpen(trimmed); ok {
			fence = ch
			_ = n
			continue
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			last = trimmed
		}
	}
	return last
}

// endsWithColon 报告文本是否以半角或全角冒号收尾（对应 killi 的 ends_with_colon）。
func endsWithColon(line string) bool {
	return strings.HasSuffix(line, ":") || strings.HasSuffix(line, "：")
}

// looksUnfinished 判定一次最终回复是否像是"承诺了但没做"。
// 问句结尾不触发，避免打断用户提问；纯冒号与意图短句都算。
func looksUnfinished(text string) bool {
	line := lastVisibleLine(text)
	if line == "" {
		return false
	}
	if strings.HasSuffix(line, "?") || strings.HasSuffix(line, "？") {
		return false
	}
	if endsWithColon(line) {
		return true
	}
	for _, marker := range unfinishedIntentMarkers {
		if strings.HasPrefix(line, marker) {
			return true
		}
	}
	return false
}

// maybeUnfinishedNudge 在轮末注入一次性续做提醒。
// 每轮至多一次（由调用点的单次调用保证）、每运行至多一次、仅顶层运行。
func (s *runState) maybeUnfinishedNudge(text string) bool {
	if unfinishedOff() || s.depth != 0 || s.unfinishedNudged || !looksUnfinished(text) {
		return false
	}
	s.unfinishedNudged = true
	s.injectSteeringAs(nudgePayload, unfinishedReminder)
	return true
}

// visibleRunes 供测试确认提醒长度有界。
func visibleRunes(text string) int { return utf8.RuneCountInString(text) }
