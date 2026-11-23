package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 能力分键授权（TODO #8）：审批授予不再只按工具名/命令前缀复用，
// 而是按 "capability\0canonicalPath" 分桶。read 的 always 授予无法被
// write/edit 复用，符号链接与真实路径共享同一把键，目录键带尾部分隔符
// 以便子路径沿用祖先授予。

// Capability 是路径能力的三类划分。
type Capability string

const (
	CapabilityRead  Capability = "read"
	CapabilityWrite Capability = "write"
	CapabilityExec  Capability = "exec"
)

var execToolNames = map[string]bool{
	"bash": true, "powershell": true, "exec": true, "shell": true,
	"run": true, "write_stdin": true, "pty": true,
}

var writeToolNames = map[string]bool{
	"write": true, "edit": true, "multi_edit": true, "apply_patch": true,
	"patch": true, "undo": true, "restore": true, "notebook_edit": true,
	"save": true, "create": true, "delete": true, "mv": true, "cp": true,
}

// CapabilityForTool 推断工具对路径的能力。未知工具按 read 处理：过于宽松
// 会把写工具误判为只读，这里只对已知写/执行工具收紧，避免误伤 MCP 工具。
func CapabilityForTool(tool string) Capability {
	name := strings.ToLower(strings.TrimSpace(tool))
	if execToolNames[name] || strings.Contains(name, "_exec") || strings.Contains(name, "__bash") {
		return CapabilityExec
	}
	if writeToolNames[name] || MutatingTool(name) {
		return CapabilityWrite
	}
	return CapabilityRead
}

// ScopeForPath 返回 (能力, 真实路径) 的规范化审批键。
//
// 真实路径由 ResolveRealPath 给出：它跟随符号链接与 junction，并对尚不
// 存在的尾部逐级向上解析到最深已存在祖先，因此新建的子文件会沿用其父
// 目录的授予。Windows 下路径统一小写。解析失败时返回空串，让持久化
// 审批器回落到工具名（更粗粒度，但绝不会更宽松）。
func ScopeForPath(tool, path string) string {
	real, err := ResolveRealPath(path)
	if err != nil {
		return ""
	}
	if runtime.GOOS == "windows" {
		real = strings.ToLower(real)
	}
	dir := strings.HasSuffix(strings.TrimSpace(path), "/") ||
		strings.HasSuffix(strings.TrimSpace(path), `\`)
	if !dir {
		if info, statErr := os.Stat(real); statErr == nil && info.IsDir() {
			dir = true
		}
	}
	if dir && !strings.HasSuffix(real, string(filepath.Separator)) {
		real += string(filepath.Separator)
	}
	return string(CapabilityForTool(tool)) + "\x00" + real
}

// scopeAncestors 返回作用域本身及其祖先目录作用域（都带尾部分隔符）。
// 它让"授予父目录"能被尚未创建的子路径沿用，也让孩子上的粘性 deny
// 覆盖其后代。非路径作用域（工具名/命令前缀）原样返回。
func scopeAncestors(scope string) []string {
	capability, path, ok := strings.Cut(scope, "\x00")
	if !ok || path == "" {
		return []string{scope}
	}
	out := []string{scope}
	current := strings.TrimRight(path, `/\`)
	for i := 0; i < 32; i++ {
		parent := filepath.Dir(current)
		if parent == current || parent == "." {
			break
		}
		if !strings.HasSuffix(parent, string(filepath.Separator)) {
			parent += string(filepath.Separator)
		}
		out = append(out, capability+"\x00"+parent)
		trimmed := strings.TrimRight(parent, `/\`)
		if trimmed == current {
			break
		}
		current = trimmed
	}
	return out
}

// scopeFromArgs 从工具的 JSON 参数中取第一个路径并生成审批键，可直接作为
// NewFileBackedScopedApprover 的 scopeFor 回调。参数畸形或无路径时返回空串，
// 审批器按既有约定回落到工具名。
func scopeFromArgs(tool, args string) string {
	scopes := scopesFromArgs(tool, args)
	if len(scopes) == 0 {
		return ""
	}
	return scopes[0]
}

// scopesFromArgs 返回参数中所有路径的审批键，去重且保持顺序，可作为
// NewFileBackedScopedApproverHierarchical 的 scopesFor 回调。
func scopesFromArgs(tool, args string) []string {
	fields, err := parseArgs(json.RawMessage(args))
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	add := func(path string) {
		scope := ScopeForPath(tool, path)
		if scope == "" || seen[scope] {
			return
		}
		seen[scope] = true
		out = append(out, scope)
	}
	for _, key := range pathArgKeys {
		if value, ok := fields[key].(string); ok {
			add(value)
		}
	}
	for _, key := range pathArrayKeys {
		if list, ok := fields[key].([]any); ok {
			for _, item := range list {
				if value, ok := item.(string); ok {
					add(value)
				}
			}
		}
	}
	return out
}
