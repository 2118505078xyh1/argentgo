package tui

import (
	"os"
	"strings"

	"example.com/argentgo/internal/i18n"
)

// language 保留迁移前的 "zh" 前缀判定：任何以 zh 开头的 locale 都用中文，
// 其余（含空值）用英文。i18n.Canonical 不认识 "zh-TW" 等地区变体，
// 直接交给它会把这类 locale 误判为英文，因此这里不调用 Normalize。
func language(locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "zh") {
		return i18n.ZhCN
	}
	return i18n.EnUS
}

// text 返回一条 TUI 字符串。全部文案已迁入 i18n 单一目录，
// 这里只做 locale 映射和 tui. 前缀转发；i18n.T 的缺失分支不再可达。
func text(locale, key string) string {
	return i18n.T(language(locale), "tui."+key)
}

func homeDir() (string, error) {
	return os.UserHomeDir()
}
