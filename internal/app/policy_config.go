package app

import (
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// policyFor 是 Execute 所用策略的唯一构造点。把它单独拆开，使配置规则的执行
// 可以在没有模型或网络的情况下测试。
//
// extraReadOnly 列出在运行时发现的、声明为只读行为的工具（当前是带有 readOnlyHint 的 MCP 工具）。
// 它们会与配置的 readOnlyTools 合并，使这些工具跳过操作人员提示。
// 配置的拒绝规则随后由 NewPolicy 应用，且仍然优先。
func policyFor(res Resolved, extraReadOnly ...string) *security.Policy {
	cfg := policyConfigFrom(res.Root, res.Cfg.Raw, res.WriteRoots, res.Sandbox)
	cfg.ReadOnlyTools = mergeReadOnlyTools(cfg.ReadOnlyTools, extraReadOnly)
	return security.NewPolicy(res.Root, cfg)
}

// mergeReadOnlyTools 把额外的只读工具名折叠进配置的列表。空值会被丢弃、重复项被去除、
// 顺序被保留，使生成的策略具有确定性。
func mergeReadOnlyTools(configured, extra []string) []string {
	out := make([]string, 0, len(configured)+len(extra))
	seen := make(map[string]bool, len(configured)+len(extra))
	add := func(name string) {
		if name = strings.TrimSpace(name); name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range configured {
		add(name)
	}
	for _, name := range extra {
		add(name)
	}
	return out
}

// policyConfigFrom 把合并配置树中面向用户的权限键解码为 security.PolicyConfig。
// 键可以是 camelCase 或 snake_case，可以位于顶层或 "permissions" 内部；嵌套键优先。
//
// 已解析的沙箱最后合并：read-only 会为每个变更型工具添加拒绝，
// 而 danger-full-access 会移除写入根目录的裁剪，但保留显式的用户规则不变。
func policyConfigFrom(root string, raw map[string]any, writeRoots []string, sandbox security.SandboxMode) security.PolicyConfig {
	lookup := policyLookup(raw)
	var cfg security.PolicyConfig
	cfg.DenyPaths = policyStrings(lookup("denyPaths", "deny_paths"))
	cfg.AllowPaths = policyStrings(lookup("allowPaths", "allow_paths"))
	cfg.DenyCommands = policyStrings(lookup("denyCommands", "deny_commands"))
	cfg.DenyCommandPaths = policyStrings(lookup("denyCommandPaths", "deny_command_paths"))
	cfg.DenyTools = policyStrings(lookup("denyTools", "deny_tools"))
	cfg.AllowTools = policyStrings(lookup("allowTools", "allow_tools"))
	cfg.ReadOnlyTools = policyStrings(lookup("readOnlyTools", "read_only_tools"))
	cfg.MutatingTools = policyStrings(lookup("mutatingTools", "mutating_tools"))
	cfg.Rules = policyRules(lookup("rules"))

	// 已解析的模式是权威；但仍会读取原始的 "sandbox" 键，
	// 使仅通过配置设置的项不会被静默忽略。
	configured := policyString(lookup("sandbox"))
	readOnly := sandbox == security.SandboxReadOnly || strings.EqualFold(configured, "read-only")
	fullAccess := sandbox == security.SandboxDangerFullAccess && !readOnly
	if fullAccess {
		cfg.WriteRoots = unrestrictedWriteRoots(root)
	} else {
		cfg.WriteRoots = append([]string(nil), writeRoots...)
		cfg.WriteRoots = append(cfg.WriteRoots, policyStrings(lookup("writeRoots", "write_roots"))...)
	}
	if readOnly {
		cfg.DenyTools = appendMutatingDenies(cfg.DenyTools, cfg.MutatingTools)
	}
	return cfg
}

// policyLookup 优先选择 permissions 对象，其次是顶层，并在每个作用域中
// 检查所有写法之后才继续。
func policyLookup(raw map[string]any) func(...string) any {
	var nested map[string]any
	for _, key := range []string{"permissions", "Permissions"} {
		if value, ok := raw[key].(map[string]any); ok {
			nested = value
			break
		}
	}
	return func(keys ...string) any {
		for _, key := range keys {
			if nested != nil {
				if value, ok := nested[key]; ok {
					return value
				}
			}
		}
		for _, key := range keys {
			if value, ok := raw[key]; ok {
				return value
			}
		}
		return nil
	}
}

// policyStrings 接受 JSON 数组、逗号分隔的字符串以及已解码的 []string 值，
// 与 security.PolicyFromJSON 的宽松程度一致。
func policyStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if text = strings.TrimSpace(text); text != "" {
					out = append(out, text)
				}
			}
		}
		return out
	case string:
		out := make([]string, 0, 4)
		for _, item := range strings.Split(typed, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	}
	return nil
}

func policyString(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func policyRules(value any) []security.Rule {
	var items []any
	switch typed := value.(type) {
	case []any:
		items = typed
	case []map[string]any:
		for _, item := range typed {
			items = append(items, item)
		}
	case []security.Rule:
		return append([]security.Rule(nil), typed...)
	}
	rules := make([]security.Rule, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		decision, ok := security.ParseDecision(policyStringField(entry, "decision", "action"))
		if !ok {
			continue
		}
		rules = append(rules, security.Rule{
			Decision: decision,
			Tool:     policyStringField(entry, "tool"),
			Path:     policyStringField(entry, "path"),
			Command:  policyStringField(entry, "command"),
		})
	}
	return rules
}

func policyStringField(entry map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := entry[key].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// unrestrictedWriteRoots 返回一个包含当前卷上所有路径（在 unix 上是整棵树）的根目录，
// 使写入边界检查成为空操作，同时每条显式的拒绝/路径规则仍然生效。
func unrestrictedWriteRoots(root string) []string {
	return []string{filepath.VolumeName(root) + string(filepath.Separator)}
}

var builtinMutatingToolNames = []string{"write", "edit", "multi_edit", "bash"}

// appendMutatingDenies 让只读模式在策略中显式体现，如同在工具注册表中一样，
// 使 MCP 和插件工具也无法做修改。
func appendMutatingDenies(deny, extra []string) []string {
	seen := make(map[string]bool, len(deny)+len(extra)+len(builtinMutatingToolNames))
	out := make([]string, 0, len(deny)+len(extra)+len(builtinMutatingToolNames))
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, name := range deny {
		add(name)
	}
	for _, name := range builtinMutatingToolNames {
		add(name)
	}
	for _, name := range extra {
		add(name)
	}
	return out
}
