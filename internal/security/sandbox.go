package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SandboxMode 是由 --sandbox-mode 选择的文件工具策略。
type SandboxMode string

const (
	// SandboxReadOnly 移除所有修改类工具。
	SandboxReadOnly SandboxMode = "read-only"
	// SandboxWorkspaceWrite 允许写入，但限于写入根目录内。
	SandboxWorkspaceWrite SandboxMode = "workspace-write"
	// SandboxDangerFullAccess 移除写入根目录边界。
	SandboxDangerFullAccess SandboxMode = "danger-full-access"
)

// FromString 解析 --sandbox-mode 的值。空字符串选择默认值 workspace-write。
func FromString(value string) (SandboxMode, error) {
	switch SandboxMode(strings.ToLower(strings.TrimSpace(value))) {
	case "":
		return SandboxWorkspaceWrite, nil
	case SandboxReadOnly:
		return SandboxReadOnly, nil
	case SandboxWorkspaceWrite:
		return SandboxWorkspaceWrite, nil
	case SandboxDangerFullAccess:
		return SandboxDangerFullAccess, nil
	}
	return "", fmt.Errorf("security: unknown sandbox mode %q (want read-only | workspace-write | danger-full-access)", value)
}

// String 返回 CLI 中的写法。
func (m SandboxMode) String() string { return string(m) }

// Valid 报告 m 是否是三种可接受模式之一。
func (m SandboxMode) Valid() bool {
	switch m {
	case SandboxReadOnly, SandboxWorkspaceWrite, SandboxDangerFullAccess:
		return true
	}
	return false
}

// IsMutatingAllowed 报告某工具在此模式下是否可以运行。只读模式只保留已知安全
// 的工具；其他模式把决定交给策略关卡，对于 workspace-write 还会交给 InWriteRoots。
func (m SandboxMode) IsMutatingAllowed(toolName string) bool {
	if m == SandboxReadOnly {
		return defaultReadOnlyTools[toolName]
	}
	return true
}

// InWriteRoots 验证 path 是否留在某个写入根目录内。相对路径相对于第一个根目录
// （通常是工作区 cwd）解析。仅有词法写法永远不够：根目录内的符号链接或 Windows
// junction 不得把写入重定向到外面，因此在边界检查之前会解析路径和每个根目录的
// 真实位置。无法解析的路径视为拒绝，而不是通过。
func InWriteRoots(path string, roots []string) error {
	if len(roots) == 0 {
		return fmt.Errorf("security: no write roots configured")
	}
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		absolute, err := filepath.Abs(expandHome(root))
		if err != nil {
			return fmt.Errorf("security: resolving write root %q: %w", root, err)
		}
		cleaned = append(cleaned, filepath.Clean(absolute))
	}
	if len(cleaned) == 0 {
		return fmt.Errorf("security: no write roots configured")
	}
	candidate := expandHome(strings.TrimSpace(path))
	if candidate == "" {
		candidate = "."
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cleaned[0], candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return fmt.Errorf("security: resolving path %q: %w", path, err)
	}
	if err := CheckPathInRoots(filepath.Clean(absolute), cleaned); err != nil {
		return fmt.Errorf("security: path %q is outside the write roots %v: %w", path, roots, err)
	}
	return nil
}

func within(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// WriteRootsFrom 是 CLI 接线用的小助手：工作区 cwd 加上任何 --add-dir 目录，
// 去重并转为绝对路径。
func WriteRootsFrom(cwd string, extra []string) []string {
	roots := make([]string, 0, len(extra)+1)
	seen := make(map[string]bool, len(extra)+1)
	add := func(value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		absolute, err := filepath.Abs(expandHome(value))
		if err != nil {
			return
		}
		absolute = filepath.Clean(absolute)
		if seen[absolute] {
			return
		}
		seen[absolute] = true
		roots = append(roots, absolute)
	}
	add(cwd)
	for _, dir := range extra {
		add(dir)
	}
	if len(roots) == 0 {
		if wd, err := os.Getwd(); err == nil {
			roots = append(roots, filepath.Clean(wd))
		}
	}
	return roots
}
