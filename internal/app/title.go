package app

import (
	"strings"
	"unicode"
)

// defaultTitleName 是空 prompt 时使用的占位会话标题。
const defaultTitleName = "新会话"

// titlePromptMinRunes 是触发 LLM 标题生成所需的最小 prompt 符文数。
// 过短的 prompt 直接沿用本地截断标题即可，不必发起模型调用。
const titlePromptMinRunes = 12

// cleanTitleText 把 prompt 归一化成适合展示的单行文本：
// 换行与其他控制字符替换为空格，连续空白合并为一个，首尾空白去掉。
func cleanTitleText(prompt string) string {
	var b strings.Builder
	b.Grow(len(prompt))
	for _, r := range prompt {
		if unicode.IsControl(r) {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// titleFromPromptFallback 从 prompt 派生一个纯本地的回退标题，无需模型调用或网络请求。
// 文本先经 cleanTitleText 归一化；空 prompt（或非正 limit）返回 defaultTitleName。
// 超长文本按 rune 截断到 limit 个符文并追加省略号“…”。
// 由于按 rune 处理，截断不会落在 UTF-8 字节中间，因此 CJK 与 emoji 都是安全的。
func titleFromPromptFallback(prompt string, limit int) string {
	cleaned := cleanTitleText(prompt)
	if cleaned == "" || limit <= 0 {
		return defaultTitleName
	}
	runes := []rune(cleaned)
	if len(runes) <= limit {
		return cleaned
	}
	return string(runes[:limit]) + "…"
}

// isDefaultTitle 判断当前标题是否仍是“尚未生成”的占位标题。
// 除空串与本包默认名外，也兼容旧路径写入的英文占位名 "session"，
// 这样无论会话来自哪条创建分支，都能被识别为需要生成标题。
func isDefaultTitle(title string) bool {
	switch strings.TrimSpace(title) {
	case "", defaultTitleName, "session":
		return true
	default:
		return false
	}
}

// shouldGenerateTitle 判断是否需要为会话生成 LLM 标题。
// 仅当当前标题仍是占位标题（默认名或空）且 prompt 足够长时返回 true，
// 避免对已有人工标题或已生成标题的会话重复发起模型调用。
func shouldGenerateTitle(prompt, currentTitle string) bool {
	if !isDefaultTitle(currentTitle) {
		return false
	}
	return len([]rune(cleanTitleText(prompt))) >= titlePromptMinRunes
}
