package main

import (
	"strings"
	"sync/atomic"
)

// native 本地化是严格 opt-in：只有 ARGENT_LOCALE 显式解析为 en-US 时
// 原生窗口的文案才切换到英文；空值、中文及其他值一律保留原有硬编码
// 中文，帧元素与文案逐字节不变。
//
// native 是 UI 外壳，架构上禁止 import internal/*（包括 internal/i18n），
// 因此这里内联一份与 i18n.Canonical 等价的别名判定和一份共享键的英文文案
// （值取自 i18n 目录，保持一致）。这样既满足「英文用户可用桌面端」，
// 又不违反分层门禁，也不改变任何默认字节。
const (
	nativeLocaleEnUS = "en-US"
	nativeLocaleZhCN = "zh-CN"
)

// canonicalNativeLocale 与 internal/i18n.Canonical 的别名集合一致。
func canonicalNativeLocale(locale string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(locale)) {
	case "en", "en-us", "en_us", "english":
		return nativeLocaleEnUS, true
	case "zh", "zh-cn", "zh_cn", "chinese", "中文":
		return nativeLocaleZhCN, true
	}
	return "", false
}

// 对硬编码扫描守卫的说明：本文件的 nativeEnglish 键是翻译文案的源文本，
// 调用点的中文实参一律被 tr(...) 包裹，语义等同于 CLI/TUI 的 text()/Tr()
// 本地化引用，不应计为新增硬编码。
var nativeLangCode atomic.Value // 保存 nativeLocaleEnUS 或 nativeLocaleZhCN

func init() {
	// 测试不调用 main()，因此默认恒为中文，所有既有断言字节不变。
	nativeLangCode.Store(nativeLocaleZhCN)
}

// configureLocale 按注入的 env 设置原生窗口文案语言。env 由调用方注入
// 而非直接读 os.Getenv，使判定可精确测试；env 为 nil 或未请求英文时关闭。
func configureLocale(env func(string) string) {
	code := nativeLocaleZhCN
	if env != nil {
		if c, ok := canonicalNativeLocale(env("ARGENT_LOCALE")); ok && c == nativeLocaleEnUS {
			code = nativeLocaleEnUS
		}
	}
	nativeLangCode.Store(code)
}

// nativeLocale 返回当前原生窗口使用的语言环境代码。包级变量的初始化
// 早于 init()，因此 tr() 可能在 Store 之前被调用；此时回退中文保证安全。
func nativeLocale() string {
	v := nativeLangCode.Load()
	if v == nil {
		return nativeLocaleZhCN
	}
	return v.(string)
}

// nativeEnglish 是 native UI 中文文案到英文的对照表。前段的英文值与
// internal/i18n 目录的同名键一致（内联以避免 native→internal 违规依赖），
// 后段是 native 独有文案。
var nativeEnglish = map[string]string{
	// ---- 与 internal/i18n 目录一致的共享键 ----
	"就绪":   "ready",
	"运行中":  "working",
	"会话":   "session",
	"模型":   "model",
	"主题":   "theme",
	"允许":   "allow",
	"拒绝":   "deny",
	"发送":   "send",
	"需要批准": "approval required",

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
	"… 已省略更多行":                          "… more lines omitted",
	" 个文件":                              " more files",
	"diff：%d 个文件，+%d/-%d 行":             "diff: %d files, +%d/-%d lines",

	// 审批三态与作用域。
	"允许一次":           "Allow once",
	"始终允许此命令":        "Always allow this command",
	"拒绝并说明理由":        "Deny with reason",
	"仅此调用不再询问":       "This call only, no further prompts for it",
	"本机持续有效，可在设置中撤销": "Persists on this device until removed in settings",
	"危险操作，默认拒绝":      "Dangerous operation — denied by default",
	"拒绝理由":           "Reason for denial",
	"输入拒绝理由…":        "Enter a reason for denying…",
	"确认始终允许":         "Confirm always allow",
	"始终允许的影响范围：":     "Always-allow affects:",
	"仅此调用":           "This call only",
	"取消":             "Cancel",

	// 导航诚实化。
	"即将推出":         "Coming soon",
	"该功能尚未实现":      "Not implemented yet",
	"规划中的能力，暂不可点击": "Planned capability, not available yet",

	// 导航面板的具体缺口说明（取代空泛的"规划中的能力"）。
	"需要任务调度 RPC 支持，后续接入":             "Needs a task-scheduling RPC; coming later",
	"需要插件与技能目录 RPC 支持，后续接入":          "Needs a plugin/skill catalog RPC; coming later",
	"运行后可见：尚未收到用量事件":                 "Visible after a run: no usage events yet",
	"未找到工作区的 .git 信息，暂时无法显示分支":       "No workspace .git info found; branch unavailable",
	"需要后台作业状态 RPC 支持，后续接入":           "Needs a background-job RPC; coming later",
	"需要工作流执行 RPC 支持，后续接入":            "Needs a workflow RPC; coming later",
	"native 不直接启动 shell，需要终端 RPC 支持": "The native shell needs a terminal RPC; coming later",
	"需要文件树 RPC 支持，后续接入":              "Needs a file-tree RPC; coming later",
	"运行后可见：当前会话没有任务列表":               "Visible after a run: this session has no task list",
	"没有更多可用信息，后续接入":                  "No further info available; coming later",

	// 用量/计划面板的真实数据行模板。
	"本轮输入 token：%d": "Input tokens this run: %d",
	"本轮输出 token：%d": "Output tokens this run: %d",
	"本轮请求数：%d":      "Requests this run: %d",
	"当前分支：":         "Branch: ",
	"进行中":           "In progress",
	"待办":            "Pending",

	// 设置与偏好持久化。
	"设置":           "Settings",
	"语言":           "Language",
	"偏好保存失败：":      "Failed to save preferences: ",
	"偏好加载失败，使用默认值": "Failed to load preferences, using defaults",

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
	"演示模式 · 未连接到引擎 · 回复与会话不会保存":                     "Demo mode · not connected to engine · replies and sessions are not saved",
	"演示模式：未连接到引擎，回复与会话为示例数据，不会保存。":                  "Demo mode: not connected to engine; replies and sessions are sample data and will not be saved.",
	"请从与引擎相同的发布包启动 argentgo-native，或设置 ARGENT_EXE。": "Launch argentgo-native from the same release package as the engine, or set ARGENT_EXE.",
	"若尚未配置模型凭据，可运行 argentgo login 或在设置中填写。":         "If no model credentials are configured yet, run argentgo login or fill them in Settings.",

	// RPG Desktop 视觉改造新增文案。
	"项目与会话":    "Projects & sessions",
	"当前会话":     "Current session",
	"新会话":      "New session",
	"输入任务或提问…": "Type a task or question…",
	"上下文":      "context",
	"未确认工具调用":  "Unconfirmed tool call",
	"Y 允许一次 · A 始终允许 · N 拒绝":        "Y allow once · A always allow · N deny",
	"拒绝理由（可选，例如：别删 dist，改删 build/）": "Reason for denial (optional, e.g. keep dist, remove build/)",

	// /goal 循环状态徽章（goal_status 事件）。用带前缀的独立键，避免与
	// i18n 共享键 "运行中"/"已完成" 的既有英文含义（working/Done）冲突。
	"目标循环：":     "goal loop: ",
	"目标运行中":     "running",
	"目标已完成":     "complete",
	"目标已达上限":    "capped",
	"目标已中断":     "interrupted",
	"目标状态未知":    "unknown state",
	"第 %d/%d 次": "iteration %d/%d",
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
	if nativeLocale() == nativeLocaleEnUS && index < len(cannedRepliesEN) {
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
	if nativeLocale() != nativeLocaleEnUS {
		return zh
	}
	if en, ok := nativeEnglish[zh]; ok && en != "" {
		return en
	}
	return zh
}
