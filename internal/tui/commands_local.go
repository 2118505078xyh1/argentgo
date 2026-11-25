package tui

import (
	"fmt"
	"strings"

	"example.com/argentgo/internal/i18n"
)

// 本文件是 TUI 原生斜杠命令的单一注册点：补全列表、参数提示、本地化描述
// 与执行体都从 nativeCommands 生成。新增一条命令只需在这里加一项，不必
// 再同步改 commands_local.go / model_input.go / 帮助文案三处。

// nativeCommand 是一条 TUI 自己解释的命令。Run 在 Model 锁内调用，
// 可以直接访问模型字段；返回 KeyAction 让 run 循环决定后续动作。
type nativeCommand struct {
	Name    string
	ArgHint string
	Run     func(*Model, string) KeyAction
}

// nativeCommands 的顺序即补全列表顺序：常用入口在前，pi 对齐入口在后。
var nativeCommands = []nativeCommand{
	{Name: "help", Run: runHelpCommand},
	{Name: "clear", Run: runClearCommand},
	{Name: "expand", Run: runExpandCommand},
	{Name: "undo", ArgHint: "[--steps N]", Run: runUndoCommand},
	{Name: "retry", ArgHint: "[--steps N]", Run: runRetryCommand},
	{Name: "name", ArgHint: "<title>", Run: runNameCommand},
	{Name: "rename", ArgHint: "<title>", Run: runNameCommand},
	{Name: "quit", Run: runQuitCommand},
	{Name: "exit", Run: runQuitCommand},
	{Name: "session", Run: runSessionsCommand},
	{Name: "sessions", Run: runSessionsCommand},
	{Name: "models", Run: runModelsCommand},
	{Name: "thinking", Run: runThinkingCommand},
	{Name: "compact", Run: runCompactCommand},
	{Name: "hotkeys", Run: runHelpCommand},
	{Name: "resume", Run: runResumeCommand},
}

// tuiNativeCommands 是去别名后的命令名列表，供补全与诊断使用。
var tuiNativeCommands = func() []string {
	names := make([]string, 0, len(nativeCommands)+1)
	for _, cmd := range nativeCommands {
		if cmd.Name == "exit" || cmd.Name == "sessions" || cmd.Name == "rename" {
			continue // 别名：不单独占据补全行
		}
		names = append(names, cmd.Name)
	}
	return names
}()

// lookupNativeCommand 按命令名查找注册项。名字不含前导斜杠。
func lookupNativeCommand(name string) (nativeCommand, bool) {
	for _, cmd := range nativeCommands {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return nativeCommand{}, false
}

// --- 各命令的执行体。保持短小，只做一件事，便于单独测试。---

func runHelpCommand(m *Model, _ string) KeyAction {
	// 每一行都是一条独立 notice，宽度按当前正文宽度裁剪。
	for _, line := range m.helpNoticeLocked() {
		m.appendNoticeLocked(line)
	}
	return ActionNone
}

func runClearCommand(m *Model, _ string) KeyAction {
	m.entries = nil
	m.entryCache = nil
	m.scroll = 0
	m.version++
	m.markDirtyLocked()
	return ActionNone
}

func runExpandCommand(m *Model, _ string) KeyAction {
	m.expanded = !m.expanded
	m.version++
	m.markDirtyLocked()
	if m.expanded {
		m.setStatusLocked(text(m.locale, "expanded"))
	} else {
		m.setStatusLocked(text(m.locale, "collapsed"))
	}
	return ActionNone
}

func runUndoCommand(m *Model, arg string) KeyAction {
	return m.requestActionLocked(ActionUndo, arg)
}

func runRetryCommand(m *Model, arg string) KeyAction {
	return m.requestActionLocked(ActionRetry, arg)
}

func runResumeCommand(m *Model, _ string) KeyAction {
	// pi 的 /resume 选择会话；这里把会话栏切到前台，回车即恢复。
	m.showSessions = true
	m.sessionFiltering = false
	if len(m.sessions) == 0 {
		m.setStatusLocked(text(m.locale, "sessions_empty"))
	} else {
		m.setStatusLocked(text(m.locale, "session_resume_prompt"))
	}
	return ActionNone
}

func runNameCommand(m *Model, arg string) KeyAction {
	if arg == "" {
		m.setStatusLocked(text(m.locale, "name_usage"))
		return ActionNone
	}
	return m.requestActionLocked(ActionRename, arg)
}

func runQuitCommand(m *Model, _ string) KeyAction {
	m.quit = true
	return ActionQuit
}

func runSessionsCommand(m *Model, _ string) KeyAction {
	m.showSessions = true
	m.sessionFiltering = false
	m.version++
	if len(m.sessions) == 0 {
		m.setStatusLocked(text(m.locale, "sessions_empty"))
	} else {
		m.setStatusLocked(text(m.locale, "sessions_on"))
	}
	return ActionNone
}

// modelsInfoLocked 说明当前模型与唯一的切换入口。运行期换模型需要重建
// provider/系统提示，TUI 不假装能就地切换。
func (m *Model) modelsInfoLocked() string {
	model := strings.TrimSpace(m.model)
	if model == "" {
		model = "auto"
	}
	return fmt.Sprintf(text(m.locale, "models_info"), model)
}

func runModelsCommand(m *Model, _ string) KeyAction {
	info := m.modelsInfoLocked()
	m.appendNoticeLocked(info)
	m.setStatusLocked(info)
	return ActionNone
}

// thinkingInfoLocked 说明当前思考强度与调整入口。
func (m *Model) thinkingInfoLocked() string {
	level := strings.TrimSpace(m.thinking)
	if level == "" {
		level = "-"
	}
	return fmt.Sprintf(text(m.locale, "thinking_info"), level)
}

func runThinkingCommand(m *Model, _ string) KeyAction {
	info := m.thinkingInfoLocked()
	m.appendNoticeLocked(info)
	m.setStatusLocked(info)
	return ActionNone
}

func runCompactCommand(m *Model, arg string) KeyAction {
	return m.requestActionLocked(ActionCompact, arg)
}

// --- 补全元数据：描述来自 i18n 目录，参数提示来自上面的注册表。---

// commandDescription 返回命令名对应的本地化描述。i18n.T 对未知键原样返回键名，
// 据此判断目录里是否登记过，避免把 "tui.cmd_x_desc" 漏到界面上。
func commandDescription(locale, name string) (string, bool) {
	key := "tui.cmd_" + name + "_desc"
	desc := i18n.T(language(locale), key)
	if desc == key {
		return "", false
	}
	return desc, true
}

// localCommandInfo 把一条命令元数据本地化：描述命中目录则替换，参数提示
// 宿主优先、缺失时回落到注册表，保证原生命令也有提示。
func localCommandInfo(locale string, info CommandInfo) CommandInfo {
	out := info
	if desc, ok := commandDescription(locale, info.Command); ok {
		out.Description = desc
	}
	if out.ArgumentHint == "" {
		if cmd, ok := lookupNativeCommand(info.Command); ok {
			out.ArgumentHint = cmd.ArgHint
		}
	}
	return out
}

// mergeCommandInfo 把 TUI 原生命令放在宿主命令之前并去重：同名命令以宿主
// （真实实现）为准，避免补全列表出现两条一模一样的命令。
func mergeCommandInfo(locale string, host []CommandInfo) []CommandInfo {
	merged := make([]CommandInfo, 0, len(tuiNativeCommands)+len(host))
	seen := make(map[string]struct{}, len(tuiNativeCommands)+len(host))
	for _, row := range host {
		seen[row.Command] = struct{}{}
	}
	for _, name := range tuiNativeCommands {
		if _, ok := seen[name]; ok {
			continue
		}
		merged = append(merged, localCommandInfo(locale, CommandInfo{Command: name}))
	}
	for _, row := range host {
		merged = append(merged, localCommandInfo(locale, row))
	}
	return merged
}
