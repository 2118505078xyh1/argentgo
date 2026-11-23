package main

import (
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/i18n"
)

// native 本地化是严格 opt-in：只有 ARGENT_LOCALE 显式解析为 en-US 时
// 原生窗口的文案才切换到英文；空值、中文及其他值一律保留原有硬编码
// 中文，帧元素与文案逐字节不变。这样 i18n 复用不会改变默认行为，
// 既满足「英文用户可用桌面端」，也不破坏现有截图/测试语义。
//
// 语言环境判定复用 internal/i18n 的 Canonical/EnUS/ZhCN；英文文案凡
// i18n 目录已有对应键的，直接取 i18n.T(i18n.EnUS, key)，使 native 与
// CLI/TUI 共用同一份目录；仅 native 独有文案才在本文件写死英文。
//
// 对硬编码扫描守卫的说明：本文件的 nativeEnglish 键是翻译目录的源文案，
// 调用点的中文实参一律被 tr(...) 包裹，语义等同于 CLI/TUI 的 text()/Tr()
// 本地化引用，不应计为新增硬编码。
var nativeLangCode atomic.Value // 保存 i18n.EnUS 或 i18n.ZhCN

func init() {
	// 测试不调用 main()，因此默认恒为中文，所有既有断言字节不变。
	nativeLangCode.Store(i18n.ZhCN)
}

// configureLocale 按注入的 env 设置原生窗口文案语言。env 由调用方注入
// 而非直接读 os.Getenv，使判定可精确测试；env 为 nil 或未请求英文时关闭。
func configureLocale(env func(string) string) {
	code := i18n.ZhCN
	if env != nil {
		if c, ok := i18n.Canonical(env("ARGENT_LOCALE")); ok && c == i18n.EnUS {
			code = i18n.EnUS
		}
	}
	nativeLangCode.Store(code)
}

// nativeLocale 返回当前原生窗口使用的 i18n 语言环境代码。包级变量的初始化
// 早于 init()，因此 tr() 可能在 Store 之前被调用；此时回退中文保证安全。
func nativeLocale() string {
	v := nativeLangCode.Load()
	if v == nil {
		return i18n.ZhCN
	}
	return v.(string)
}

// catalogEN 取 i18n 目录中某键的英文文案，使 native 与 CLI/TUI 共享目录。
func catalogEN(key string) string { return i18n.T(i18n.EnUS, key) }

// nativeEnglish 是 native UI 中文文案到英文的对照表。前段复用 i18n 目录
// （英文值由 catalogEN 取得），后段是 native 独有、目录中暂无对应键的文案。
var nativeEnglish = map[string]string{
	// ---- 复用 internal/i18n 目录 ----
	"就绪":   catalogEN("status.ready"),
	"运行中":  catalogEN("status.working"),
	"会话":   catalogEN("label.session"),
	"模型":   catalogEN("label.model"),
	"主题":   catalogEN("tui.theme"),
	"允许":   catalogEN("tui.allow"),
	"拒绝":   catalogEN("tui.deny"),
	"发送":   catalogEN("prompt.send"),
	"需要批准": catalogEN("approval.title"),

	// ---- native 独有文案 ----
	"聊天":        "Chat",
	"定时任务":      "Scheduled tasks",
	"文档":        "Docs",
	"记忆":        "Memory",
	"扩展":        "Extensions",
	"用量":        "Usage",
	"任务":        "Tasks",
	"工作流":       "Workflows",
	"终端":        "Terminal",
	"文件":        "Files",
	"计划":        "Plan",
	"重构聊天界面":    "Refactor chat UI",
	"2 分钟前":     "2 min ago",
	"今天":        "Today",
	"修复工具调用状态":  "Fix tool call status",
	"昨天 18:20":  "Yesterday 18:20",
	"昨天":        "Yesterday",
	"整理记忆索引":    "Organize memory index",
	"3 天前":      "3 days ago",
	"更早":        "Earlier",
	"解释这段代码":    "Explain this code",
	"写单元测试":     "Write unit tests",
	"检查命令":      "Check command",
	"/ 命令":      "/ commands",
	"已切换到":      "Switched to ",
	"搜索会话":      "Search sessions",
	"会话列表加载失败：": "Failed to load sessions: ",
	"刚刚":        "just now",
	"%d 分钟前":    "%d min ago",
	"%d 小时前":    "%d hours ago",
	"%d 天前":     "%d days ago",
	"已打开会话：":    "Opened session: ",
	"打开会话失败：":   "Failed to open session: ",
	"打开会话失败：无法解析会话记录": "Failed to open session: cannot parse records",
	"撤销":       "Undo",
	"重试":       "Retry",
	"有什么可以帮你？": "How can I help?",
	"问点什么，或者输入 / 看命令。":                  "Ask anything, or type / for commands.",
	"首次使用请在终端运行 argentgo login 配置模型凭据。": "First time here? Run 'argentgo login' in a terminal to configure model credentials.",
	"Enter 发送 · Shift+Enter 换行":         "Enter to send · Shift+Enter for newline",
	"停止":                                "Stop",
	"Y 允许 · N 拒绝":                       "Y allow · N deny",
	"你":                                 "You",
	"正在生成…":                             "Generating…",
	"引擎错误":                              "engine error",
	"引擎连接已断开":                           "Engine connection lost",
	"需要批准: ":                            "approval required: ",
	"✓ 已允许 ":                            "✓ allowed ",
	"✗ 已拒绝 ":                            "✗ rejected ",
	"已完成":                               "Done",
	"已停止":                               "Stopped",
	"没有可撤销的消息":                          "Nothing to undo",
	"已撤销上一轮":                            "Undid last turn",
	"正在生成，无法重试":                         "Cannot retry while generating",
	"没有可重试的消息":                          "Nothing to retry",
	"已重试上一轮":                            "Retried last turn",
	"已切换到浅色主题":                          "Switched to light theme",
	"已切换到深色主题":                          "Switched to dark theme",
	"审批回应失败：":                           "Approval answer failed: ",
	"已响应，等待引擎确认":                        "Responded, waiting for engine",
	"已新建会话":                             "New session created",
	"已连接引擎":                             "Engine connected",
	"⚠ 引擎错误: ":                          "⚠ engine error: ",
	"… 还有 ":                             "… and ",
	" 个文件":                              " more files",
	"diff：%d 个文件，+%d/-%d 行":             "diff: %d files, +%d/-%d lines",

	// nav 占位面板（renderNavPanel 在渲染时翻译）。
	"查看并按计划运行自动化任务。":    "View and run scheduled automation tasks.",
	"暂无已配置的任务":          "No tasks configured",
	"可用 CLI 添加 cron 任务": "Add cron tasks via the CLI",
	"浏览工作区内的项目文档与说明。":   "Browse project docs and guides in the workspace.",
	"管理跨会话注入的项目记忆。":     "Manage project memory injected across sessions.",
	"查看已安装的插件与技能。":      "View installed plugins and skills.",
	"插件":                "Plugins",
	"技能":                "Skills",
	"MCP 服务器":           "MCP servers",
	"统计令牌消耗与请求次数。":      "Track token usage and request counts.",
	"本次会话":              "This session",
	"今日汇总":              "Today's summary",
	"查看仓库状态与最近提交。":      "View repository status and recent commits.",
	"工作区变更":             "Workspace changes",
	"提交历史":              "Commit history",
	"分支":                "Branches",
	"跟踪后台任务与长时作业。":      "Track background jobs and long-running tasks.",
	"正在运行":              "Running",
	"最近完成":              "Recently completed",
	"运行可复用的多步骤脚本。":      "Run reusable multi-step workflows.",
	"可用工作流":             "Available workflows",
	"运行记录":              "Run history",
	"在工作区内打开一个 shell。":  "Open a shell in the workspace.",
	"浏览当前工作区的文件树。":      "Browse the current workspace file tree.",
	"工作区目录":             "Workspace directory",
	"最近打开":              "Recently opened",
	"查看并整理多步骤的实施计划。":    "View and organize multi-step implementation plans.",
	"当前计划":              "Current plan",
	"待办步骤":              "Pending steps",
	"该面板尚未实现。":          "This panel is not implemented yet.",
	"敬请期待":              "Coming soon",
}

// cannedRepliesEN 是 main.go 中 demo 预设回复的英文版，索引一一对应。
// 单独成表而非塞进 tr 映射，避免在包级变量初始化阶段固化语言。
var cannedRepliesEN = []string{
	"## All done\n\nI understand your question. This is a **native Markdown** reply:\n\n" +
		"- Supports lists\n- Supports `code` and [links](https://example.com)\n\n" +
		"```go\nfmt.Println(\"hello, argentgo\")\n```\n\n" +
		"> Streaming and Markdown rendering are both handled by ui/markdown.\n",
	"## Analysis complete\n\n| Item | Status |\n|---|---|\n| Session | ready |\n| Tools | approved |\n\n" +
		"1. Read context\n2. Generate reply\n\n*Keep the questions coming.*\n",
}

// localizedReply 按当前语言返回第 index 条 demo 预设回复；越界时回退空串，
// 调用方始终以 cannedReplies 的长度取模，因此越界不会发生。
func localizedReply(index int) string {
	if nativeLocale() == i18n.EnUS && index < len(cannedRepliesEN) {
		return cannedRepliesEN[index]
	}
	if index < len(cannedReplies) {
		return cannedReplies[index]
	}
	return ""
}

// tr 返回 zh 文案在当前语言环境下的显示文本。默认（中文/未开启）时
// 原样返回入参，因此所有既有渲染路径保持逐字节不变。表中缺失的键同样
// 原样返回，避免新文案在英文环境下变成空白。
func tr(zh string) string {
	if nativeLocale() != i18n.EnUS {
		return zh
	}
	if en, ok := nativeEnglish[zh]; ok && en != "" {
		return en
	}
	return zh
}
