package tui

import "github.com/2118505078xyh1/argentgo/internal/i18n"

// 斜杠命令的名称、描述与参数提示。
//
// 描述统一走 internal/i18n 的 `tui.cmd_<name>_desc` 目录：TUI 自己解释的
// 命令（native）与宿主注入的命令（internal/commands 的 /context、/review）
// 用同一套中英两栏文案，因此中文界面下补全列表里不会混进英文短句。目录里
// 没有登记的命令保留宿主原文——不猜测、不改写未知命令。

// commandArgHints 只登记带参数的命令。提示本身是 CLI 字面量，不做翻译，
// 免得用户照着提示敲却敲不出命令。
var commandArgHints = map[string]string{
	"undo":  "[--steps N]",
	"retry": "[--steps N]",
	"name":  "<title>",
}

// tuiNativeCommands 是 TUI 在自己的命令分派里解释的斜杠命令，不经过
// internal/commands。顺序即补全列表顺序：先放最常用的入口。
var tuiNativeCommands = []string{
	"help", "clear", "expand", "undo", "retry", "name", "quit",
	// 与 pi 对齐的常用入口。面板类命令复用已有面板，不引入新窗口。
	"sessions", "models", "thinking", "compact", "hotkeys", "resume",
}

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
// 宿主优先、缺失时回落到本地表，保证原生命令也有提示。
func localCommandInfo(locale string, info CommandInfo) CommandInfo {
	out := info
	if desc, ok := commandDescription(locale, info.Command); ok {
		out.Description = desc
	}
	if out.ArgumentHint == "" {
		out.ArgumentHint = commandArgHints[info.Command]
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
