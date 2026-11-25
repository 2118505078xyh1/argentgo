package tui

import (
	"fmt"

	"example.com/argentgo/internal/i18n"
)

// visibleNoticeText 把关键通知的正文按 locale 调整后再展示。
//
// 轮次中压缩由 agent 以英文固定格式发出（compacted context: ~X -> ~Y tokens），
// 若原样上线，中文用户只看到一串英文数字而不知道"上下文被压缩了"。这里按稳定
// 前缀把这一条翻译成"上下文已压缩：X→Y tokens"，其余通知（压缩后重试的中文
// 文本、预算提醒等）原样透传。解析失败时也原样透传——宁可显示英文原文，
// 也绝不因为格式漂移而丢失这条本就不可丢弃的通知。
func visibleNoticeText(locale, detail string) string {
	var before, after, target int
	n, _ := fmt.Sscanf(detail, "compacted context: ~%d -> ~%d tokens (target %d)",
		&before, &after, &target)
	if n != 3 {
		return detail
	}
	if language(locale) == i18n.ZhCN {
		return fmt.Sprintf("上下文已压缩：%d→%d tokens（目标 %d）", before, after, target)
	}
	return detail
}
