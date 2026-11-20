package tools

import (
	"strconv"
	"unicode/utf8"
)

// tooManyFilesWarningMaxRunes 是告警文本的 rune 上限。无论传入的数字有多大，
// 返回值都不会超过这个长度，从而有界、不会撑爆上下文。
const tooManyFilesWarningMaxRunes = 256

// tooManyFilesWarning 在批量加入文件的数量或估算 token 超过阈值时，返回一条
// 模型可见的中文告警，提醒"只加入需要改动的文件"；未超过阈值时返回空串。
//
// 比较使用严格大于：恰好等于阈值不算越界。maxFiles / maxTokens 为非正数时
// 表示对应维度不设限、永不触发；两者都不设限时恒返回空串。
//
// 该函数是纯函数，不持有任何状态。"一次性告警"由调用方自行去重（例如在会话中
// 记录一个已告警标记）；本函数每次调用都只依据当前参数给出结果，因此可被安全
// 地反复调用，也适合在 -race 下并发使用。
//
// 返回值按 rune 边界裁剪，长度不超过 tooManyFilesWarningMaxRunes 个 rune，
// 始终是合法 UTF-8，因此 CJK 安全且有界。
func tooManyFilesWarning(fileCount int, totalTokens int, maxFiles int, maxTokens int) string {
	tooManyFiles := maxFiles > 0 && fileCount > maxFiles
	tooLarge := maxTokens > 0 && totalTokens > maxTokens
	if !tooManyFiles && !tooLarge {
		return ""
	}

	var msg string
	switch {
	case tooManyFiles && tooLarge:
		msg = "警告：批量加入的文件过多（" + strconv.Itoa(fileCount) + " 个，上限 " + strconv.Itoa(maxFiles) +
			"），且内容过大（约 " + strconv.Itoa(totalTokens) + " tokens，上限 " + strconv.Itoa(maxTokens) +
			"）；请只加入需要改动的文件。"
	case tooManyFiles:
		msg = "警告：批量加入的文件过多（" + strconv.Itoa(fileCount) + " 个，上限 " + strconv.Itoa(maxFiles) +
			"）；请只加入需要改动的文件。"
	default: // 仅 token 越界
		msg = "警告：批量加入的内容过大（约 " + strconv.Itoa(totalTokens) + " tokens，上限 " + strconv.Itoa(maxTokens) +
			"）；请只加入需要改动的文件。"
	}
	return clipWarningRunes(msg, tooManyFilesWarningMaxRunes)
}

// clipWarningRunes 在 rune 边界把 s 截断到最多 max 个 rune；超出时去掉末尾一个
// rune 并追加省略号 "…"，因此返回值始终是合法 UTF-8、不会切坏 CJK 字符且长度有界。
func clipWarningRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	limit := max - 1 // 为省略号预留一个 rune
	out := make([]rune, 0, limit)
	for _, r := range s {
		if len(out) == limit {
			break
		}
		out = append(out, r)
	}
	return string(out) + "…"
}
