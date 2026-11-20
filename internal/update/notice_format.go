package update

import "strings"

// maxNoticeNoteLines 限制更新公告中展示的版本说明行数，
// 使公告在终端里保持简短。
const maxNoticeNoteLines = 5

// maxNoticeNoteLineRunes 限制版本说明每一行的 rune 数，
// 超长行按 rune 截断（对 CJK 安全），避免撑破终端宽度。
const maxNoticeNoteLineRunes = 80

// noticeMoreMarker 在说明行数超过上限时作为最后一行，
// 表示后面还有未展示的内容。
const noticeMoreMarker = "…"

// formatUpdateNotice 生成一条用于展示的更新公告。
//
// 当 latest 比 current 新时，首行以"新版本 … 可用（当前 …）"说明版本，
// 随后附上版本说明（notes）解析后的前若干行：先是一条摘要，再由变更项
// 组成的列表。当 notes 为空（或解析后没有任何可展示内容）时，只返回首行的
// 简化文案。
//
// 版本号经 ASCII 白名单净化，防止发布元数据注入控制字符；说明文本先尝试用
// extractChangelogSection 抽取 latest 对应的段落，再交给 ParseReleaseNotes
// 结构化，最后按行数与 rune 数双重有界截断，对 CJK 文本按 rune 安全处理，
// 不会在多字节字符中间切分。
//
// 当 latest 不比 current 新时，返回一条简短的"已是最新版本"文案。
//
// 本函数为纯函数：无 I/O、无全局状态，相同输入总是产生相同输出。
func formatUpdateNotice(current, latest, notes string) string {
	safeCurrent := safeVersionToken(current)
	safeLatest := safeVersionToken(latest)
	if compareVersions(latest, current) <= 0 {
		return "已是最新版本"
	}
	header := "新版本 " + safeLatest + " 可用（当前 " + safeCurrent + "）"
	lines := noticeNoteLines(notes, safeLatest)
	if len(lines) == 0 {
		return header
	}
	return header + "\n" + strings.Join(lines, "\n")
}

// noticeNoteLines 将版本说明解析为有界的多行文本。
// 若 notes 是包含多个版本段落的完整变更日志，则只取 version 对应的段落；
// 否则整段都作为发布正文。解析后的摘要与变更项先折叠为单行，再按
// maxNoticeNoteLines 行与每行 maxNoticeNoteLineRunes 个 rune 截断。
func noticeNoteLines(notes, version string) []string {
	body := notes
	if section := extractChangelogSection(notes, version); section != "" {
		body = section
	}
	parsed := ParseReleaseNotes(body)

	var lines []string
	if summary := singleLine(parsed.Summary); summary != "" {
		lines = append(lines, truncateRunes(summary, maxNoticeNoteLineRunes))
	}
	for _, change := range parsed.Changes {
		text := "- " + singleLine(change)
		if strings.TrimSpace(text) == "-" {
			continue
		}
		lines = append(lines, truncateRunes(text, maxNoticeNoteLineRunes))
	}

	if len(lines) <= maxNoticeNoteLines {
		return lines
	}
	// 超出行数上限时保留前 N-1 行，末行用省略号提示还有更多内容。
	kept := append([]string(nil), lines[:maxNoticeNoteLines-1]...)
	return append(kept, noticeMoreMarker)
}
