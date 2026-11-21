package security

import (
	"sort"
	"strings"
	"unicode"
)

// 本文件下沉了通用的“工具是否修改状态”判定，供策略目录过滤、工具分类与
// MCP 执行门控共用。它只依赖标准库，保持 security 包的零依赖约束。
//
// 判定分两层：defaultMutatingTools 是精确的内置名称表；mutatingVerbs 是保守
// 的写动词启发式，用于识别带命名空间的 MCP 工具（如 mcp__server__write_file）
// 以及表外但名称显然含写动词的工具。未知名称一律视为只读，避免把新工具悄悄
// 当成可写。

// mutatingVerbs 覆盖常见的写动词及其缩写。token 按精确比较，
// 因此 "read_file" 不会被标记，而 "writeFile" 会。
var mutatingVerbs = map[string]bool{
	"add": true, "append": true, "apply": true, "bash": true, "bootstrap": true,
	"checkout": true, "chmod": true, "chown": true, "clean": true, "clear": true,
	"commit": true, "configure": true, "copy": true, "cp": true, "create": true,
	"del": true, "delete": true, "deploy": true, "disable": true, "drop": true,
	"edit": true, "enable": true, "exec": true, "execute": true, "fix": true,
	"fmt": true, "format": true, "grant": true, "init": true, "insert": true,
	"install": true, "kill": true, "migrate": true, "mkdir": true, "modify": true,
	"move": true, "mv": true, "new": true, "overwrite": true, "patch": true,
	"post": true, "publish": true, "put": true, "remove": true, "rename": true,
	"replace": true, "reset": true, "restart": true, "revoke": true, "rm": true,
	"run": true, "save": true, "seed": true, "send": true, "set": true,
	"shell": true, "spawn": true, "start": true, "stop": true, "toggle": true,
	"touch": true, "truncate": true, "uninstall": true, "unlink": true,
	"update": true, "upload": true, "upsert": true, "write": true,
}

// MutatingTool 报告 name 是否看起来是会改变状态的工具。
//
// 它先取短名（去掉 "server__" 命名空间前缀），再结合内置的精确名称表
// defaultMutatingTools 与写动词启发式。带命名空间的 MCP 工具因此能在
// 只读模式下被识别并拦截，而 read/search 等只读工具保持放行。
func MutatingTool(name string) bool {
	short := mutatingShortName(name)
	if short == "" {
		return false
	}
	if defaultMutatingTools[short] {
		return true
	}
	lowered := strings.ToLower(short)
	return mutatingVerbs[lowered] || builtinMutating(short)
}

// DefaultMutatingToolNames 返回内置精确修改类表的工具名（排序后），供需要把
// 修改类工具显式写入策略拒绝名单的调用方共用，避免两处名单漂移。
func DefaultMutatingToolNames() []string {
	names := make([]string, 0, len(defaultMutatingTools))
	for name := range defaultMutatingTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// mutatingShortName 从带命名空间的工具名（"server__tool"）中取出短工具名。
// MCP 工具以 "mcp__server__tool" 形式公布，只检查最后一段。
func mutatingShortName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "__"); i >= 0 {
		name = strings.TrimSpace(name[i+2:])
	}
	return name
}

// builtinMutating 用写动词表检查工具名本身及其拆分出的词。
func builtinMutating(name string) bool {
	if mutatingVerbs[strings.ToLower(strings.TrimSpace(name))] {
		return true
	}
	for _, token := range toolTokens(name) {
		if mutatingVerbs[token] {
			return true
		}
	}
	return false
}

// toolTokens 把 snake_case、kebab-case、camelCase 以及字母/数字变化
// 拆分成小写单词，而不引入 regexp 依赖。
func toolTokens(name string) []string {
	runes := []rune(name)
	var tokens []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, strings.ToLower(word.String()))
			word.Reset()
		}
	}
	for i, r := range runes {
		separator := !unicode.IsLetter(r) && !unicode.IsDigit(r)
		boundary := word.Len() > 0 && i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1])
		digitEdge := word.Len() > 0 && i > 0 &&
			((unicode.IsDigit(r) && unicode.IsLetter(runes[i-1])) ||
				(unicode.IsLetter(r) && unicode.IsDigit(runes[i-1])))
		if separator || boundary || digitEdge {
			flush()
		}
		if !separator {
			word.WriteRune(r)
		}
	}
	flush()
	return tokens
}
