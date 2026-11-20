package update

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxNoticeSummaryRunes 限制附加到更新通知中的发布说明摘录长度，
// 使整条消息保持紧凑的单行。
const maxNoticeSummaryRunes = 80

// placeholderVersion 替换未通过基本校验的版本字符串，使
// 不可信的发布元数据无法向展示给用户的通知中注入控制字符或
// 标记。
const placeholderVersion = "?"

// FormatUpdateNotice 返回一条经过净化的单行中文通知，
// 告知用户有更新的构建版本可用以及如何升级。它是
// 纯函数：无 I/O、无全局状态，相同输入总是产生
// 相同输出。
//
// 两个版本字符串都按保守的 ASCII 白名单检查；
// 其他任何内容（控制字符、空格、ANSI 转义、标记）都变成
// "?" 占位符。当 notes 非空时，附加一段简短的单行摘要，
// 并按 maxNoticeSummaryRunes 个 rune 截断（对 CJK 文本按 rune 安全处理）。
// 若 latest 不比 current 新，则返回一条简短的 "already up to date" 通知
// 代替。
func FormatUpdateNotice(current, latest, notes string) string {
	safeCurrent := safeVersionToken(current)
	safeLatest := safeVersionToken(latest)
	if CompareVersions(latest, current) <= 0 {
		return "已是最新版本"
	}
	notice := "有新版本 " + safeCurrent + "→" + safeLatest + "，运行 argent update 升级"
	if summary := noticeSummary(notes); summary != "" {
		notice += "：" + summary
	}
	return notice
}

// safeVersionToken 在文本是由 ASCII 字母、数字、"."、"-"、"+" 或 "_"
// 组成的合理版本标签时原样返回。其他内容
// 均替换为 placeholderVersion，使发布元数据无法注入内容。
func safeVersionToken(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(trimmed) > 64 {
		return placeholderVersion
	}
	for index := 0; index < len(trimmed); index++ {
		char := trimmed[index]
		switch {
		case char >= '0' && char <= '9',
			char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char == '.', char == '-', char == '+', char == '_':
			// 允许的版本字符
		default:
			return placeholderVersion
		}
	}
	return trimmed
}

// noticeSummary 从原始发布说明中提取简短的单行摘录。
// 它复用 ParseReleaseNotes 选取第一行散文（去除内联 markdown
// 后），若没有则回退到第一个变更项，然后规范化空白
// 并按 rune 数截断，使 CJK 字符从不被拆分。
func noticeSummary(notes string) string {
	if strings.TrimSpace(notes) == "" {
		return ""
	}
	parsed := ParseReleaseNotes(notes)
	text := parsed.Summary
	if strings.TrimSpace(text) == "" && len(parsed.Changes) > 0 {
		text = parsed.Changes[0]
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return truncateRunes(singleLine(text), maxNoticeSummaryRunes)
}

// singleLine 将每一段空白或控制字符（包括
// 换行和 ANSI 转义）折叠为一个 ASCII 空格，使结果不含
// 换行符或终端控制序列。
func singleLine(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	spacePending := false
	for _, char := range text {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			spacePending = builder.Len() > 0
			continue
		}
		if spacePending {
			builder.WriteByte(' ')
			spacePending = false
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

// truncateRunes 将文本缩短到最多 max 个 rune，若被截断则
// 去掉末尾空格并附加省略号。它对多字节 rune 安全。
func truncateRunes(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return strings.TrimRight(string(runes[:max]), " ") + "…"
}
