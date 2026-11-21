package tui

import (
	"os"
	"strings"
)

// strings 保存 UI 随附的两种区域设置；整个 harness 的用户可见文本
// 都在一张表中，因此第三种语言只是数据变更。
var localeStrings = map[string]map[string]string{
	"en": {
		"hint":                 "enter send · alt+enter newline · pgup/pgdn scroll · ctrl+c cancel · /help",
		"hint_modal":           "y allow · n deny · a allow for session · esc deny",
		"approval_title":       "approval",
		"tool":                 "tool",
		"allow":                "allow",
		"deny":                 "deny",
		"allow_session":        "allow for session",
		"danger":               "danger",
		"default_deny":         "default: deny (enter refuses)",
		"approved":             "approved",
		"approved_session":     "approved for this session",
		"denied":               "denied",
		"thinking":             "thinking",
		"working":              "working…",
		"ready":                "ready",
		"busy":                 "a turn is still running",
		"cancelling":           "cancelling…",
		"cancelled":            "cancelled",
		"expanded":             "tool output expanded",
		"collapsed":            "tool output collapsed",
		"error":                "error",
		"theme":                "theme",
		"color_degraded":       "colour output degraded (NO_COLOR or dumb terminal)",
		"help":                 "enter sends · alt+enter/ctrl+j newline · shift+up/down scroll · ctrl+c cancels, again quits · ctrl+t tasks · ctrl+b workspace · ctrl+p plan · ctrl+s sessions · ctrl+o commands · alt+u undo · alt+r retry · /clear /expand /undo /retry /name <title> /quit (markdown commands expand $ARGUMENTS / $1..$N)",
		"tasks_title":          "TASKS",
		"workspace_title":      "WORKSPACE",
		"plan_title":           "PLAN",
		"tasks_on":             "tasks panel shown (ctrl+t to hide)",
		"tasks_off":            "tasks panel hidden",
		"workspace_on":         "workspace panel shown (↑↓ move, ctrl+b to hide)",
		"workspace_off":        "workspace panel hidden",
		"plan_on":              "plan panel shown (ctrl+p to hide)",
		"plan_off":             "plan panel hidden",
		"no_tasks":             "no tool calls yet",
		"no_plan":              "no plan submitted yet (submit_plan)",
		"sessions_title":       "SESSIONS",
		"sessions_on":          "session rail shown (ctrl+s to hide)",
		"sessions_off":         "session rail hidden",
		"no_sessions":          "no sessions yet",
		"commands_title":       "COMMANDS",
		"commands_on":          "commands panel shown (ctrl+o to hide)",
		"commands_off":         "commands panel hidden",
		"no_commands":          "no commands yet",
		"undo_unwired":         "/undo needs a checkpoint hook from the host",
		"retry_unwired":        "/retry needs a checkpoint hook from the host",
		"rename_unwired":       "renaming needs a session hook from the host",
		"name_usage":           "usage: /name <title>",
		"cached":               "cached",
		"attempt_failed":       "failed",
		"attempt_retried":      "retried",
		"attempt_cancelled":    "cancelled",
		"attempt_stream_error": "stream error",
		"undo_done":            "undo finished",
		"retry_done":           "retry requested",
		"rename_done":          "session renamed",
	},
	"zh": {
		"hint":                 "enter 发送 · alt+enter 换行 · pgup/pgdn 滚动 · ctrl+c 取消 · /help",
		"hint_modal":           "y 允许 · n 拒绝 · a 本次会话允许 · esc 拒绝",
		"approval_title":       "需要确认",
		"tool":                 "工具",
		"allow":                "允许",
		"deny":                 "拒绝",
		"allow_session":        "本会话允许",
		"danger":               "危险",
		"default_deny":         "默认：拒绝（回车即拒绝）",
		"approved":             "已允许",
		"approved_session":     "本会话已允许",
		"denied":               "已拒绝",
		"thinking":             "思考",
		"working":              "思考中…",
		"ready":                "就绪",
		"busy":                 "当前回合尚未结束",
		"cancelling":           "正在取消…",
		"cancelled":            "已取消",
		"expanded":             "工具输出已展开",
		"collapsed":            "工具输出已折叠",
		"error":                "错误",
		"theme":                "主题",
		"color_degraded":       "颜色输出已降级（NO_COLOR 或 dumb 终端）",
		"help":                 "enter 发送 · alt+enter/ctrl+j 换行 · shift+up/down 滚动 · ctrl+c 取消，再按退出 · ctrl+t 任务 · ctrl+b 工作区 · ctrl+p 计划 · ctrl+s 会话 · ctrl+o 命令 · alt+u 撤销 · alt+r 重试 · /clear /expand /undo /retry /name <标题> /quit（markdown 命令支持 $ARGUMENTS / $1..$N）",
		"tasks_title":          "任务",
		"workspace_title":      "工作区",
		"plan_title":           "计划",
		"tasks_on":             "已显示任务面板（ctrl+t 隐藏）",
		"tasks_off":            "已隐藏任务面板",
		"workspace_on":         "已显示工作区面板（↑↓ 移动，ctrl+b 隐藏）",
		"workspace_off":        "已隐藏工作区面板",
		"plan_on":              "已显示计划面板（ctrl+p 隐藏）",
		"plan_off":             "已隐藏计划面板",
		"no_tasks":             "暂无工具调用",
		"no_plan":              "尚未提交计划（submit_plan）",
		"sessions_title":       "会话",
		"sessions_on":          "已显示会话列表（ctrl+s 隐藏）",
		"sessions_off":         "已隐藏会话列表",
		"no_sessions":          "暂无会话",
		"commands_title":       "命令",
		"commands_on":          "已显示命令面板（ctrl+o 隐藏）",
		"commands_off":         "已隐藏命令面板",
		"no_commands":          "暂无命令",
		"undo_unwired":         "/undo 需要宿主提供检查点钩子",
		"retry_unwired":        "/retry 需要宿主提供检查点钩子",
		"rename_unwired":       "重命名需要宿主提供会话钩子",
		"name_usage":           "用法：/name <标题>",
		"cached":               "缓存",
		"attempt_failed":       "失败",
		"attempt_retried":      "已重试",
		"attempt_cancelled":    "已取消",
		"attempt_stream_error": "流式错误",
		"undo_done":            "撤销完成",
		"retry_done":           "已请求重试",
		"rename_done":          "会话已重命名",
	},
}

func language(locale string) string {
	if strings.HasPrefix(strings.ToLower(locale), "zh") {
		return "zh"
	}
	return "en"
}

// text 返回一条 UI 字符串，先回退到英语，再回退到键名。
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
