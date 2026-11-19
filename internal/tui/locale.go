package tui

import (
	"os"
	"strings"
)

// strings holds the two locales the UI ships; the user-facing text of the
// whole harness lives in one table so a third language is a data change.
var localeStrings = map[string]map[string]string{
	"en": {
		"hint":             "enter send · alt+enter newline · pgup/pgdn scroll · ctrl+c cancel · /help",
		"hint_modal":       "y allow · n deny · a allow for session · esc deny",
		"approval_title":   "approval",
		"tool":             "tool",
		"allow":            "allow",
		"deny":             "deny",
		"allow_session":    "allow for session",
		"approved":         "approved",
		"approved_session": "approved for this session",
		"denied":           "denied",
		"thinking":         "thinking",
		"working":          "working…",
		"ready":            "ready",
		"busy":             "a turn is still running",
		"cancelling":       "cancelling…",
		"cancelled":        "cancelled",
		"expanded":         "tool output expanded",
		"collapsed":        "tool output collapsed",
		"error":            "error",
		"theme":            "theme",
		"color_degraded":   "colour output degraded (NO_COLOR or dumb terminal)",
		"help":             "enter sends · alt+enter/ctrl+j newline · shift+up/down scroll · ctrl+c cancels, again quits · /clear /expand /quit",
	},
	"zh": {
		"hint":             "enter 发送 · alt+enter 换行 · pgup/pgdn 滚动 · ctrl+c 取消 · /help",
		"hint_modal":       "y 允许 · n 拒绝 · a 本次会话允许 · esc 拒绝",
		"approval_title":   "需要确认",
		"tool":             "工具",
		"allow":            "允许",
		"deny":             "拒绝",
		"allow_session":    "本会话允许",
		"approved":         "已允许",
		"approved_session": "本会话已允许",
		"denied":           "已拒绝",
		"thinking":         "思考",
		"working":          "思考中…",
		"ready":            "就绪",
		"busy":             "当前回合尚未结束",
		"cancelling":       "正在取消…",
		"cancelled":        "已取消",
		"expanded":         "工具输出已展开",
		"collapsed":        "工具输出已折叠",
		"error":            "错误",
		"theme":            "主题",
		"color_degraded":   "颜色输出已降级（NO_COLOR 或 dumb 终端）",
		"help":             "enter 发送 · alt+enter/ctrl+j 换行 · shift+up/down 滚动 · ctrl+c 取消，再按退出 · /clear /expand /quit",
	},
}

func language(locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "zh") {
		return "zh"
	}
	return "en"
}

// text returns one UI string, falling back to English then to the key.
func text(locale, key string) string {
	table := localeStrings[language(locale)]
	if value, ok := table[key]; ok {
		return value
	}
	if value, ok := localeStrings["en"][key]; ok {
		return value
	}
	return key
}

func homeDir() (string, error) {
	return os.UserHomeDir()
}
