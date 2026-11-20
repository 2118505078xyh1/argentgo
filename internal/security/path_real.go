package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveRealPath 返回 path 在磁盘上的真实位置。它在每个平台上都跟随符号链接，
// 并在 Windows 上跟随目录 junction（filepath.EvalSymlinks 有意不解析 junction）。
//
// 尚不存在的尾部会被重新挂接到最长的已存在祖先上，因此即将创建的文件的路径
// 仍会针对其父目录的真实位置进行检查。
//
// 该函数有意采用失败即拒绝（fail-closed）：如果路径无法解析，它返回错误，
// 而不是回退到其字面拼写。调用方必须把该错误视为拒绝，而不是"大概没问题"。
func ResolveRealPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	absolute, err := filepath.Abs(expandHome(strings.TrimSpace(path)))
	if err != nil {
		return "", fmt.Errorf("resolving path %q: %w", path, err)
	}
	return resolveWithTail(filepath.Clean(absolute))
}

// CheckPathInRoots 验证 path 的真实位置是否留在 roots 之一内部。两侧都会先被
// 解析：通过链接到达的 root 仍能正常工作，而 root 内部的链接或 junction
// 无法隐藏一次逃逸。
func CheckPathInRoots(path string, roots []string) error {
	if len(roots) == 0 {
		return fmt.Errorf("security: no roots configured")
	}
	realRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		real, err := ResolveRealPath(root)
		if err != nil {
			return fmt.Errorf("security: resolving root %q: %w", root, err)
		}
		realRoots = append(realRoots, real)
	}
	if len(realRoots) == 0 {
		return fmt.Errorf("security: no roots configured")
	}
	real, err := ResolveRealPath(path)
	if err != nil {
		return fmt.Errorf("security: resolving path %q: %w", path, err)
	}
	if within(real, realRoots) {
		return nil
	}
	return fmt.Errorf("security: real path %q resolves outside the roots %v", path, roots)
}

func resolveWithTail(absolute string) (string, error) {
	if resolved, err := realPathExisting(absolute); err == nil {
		return resolved, nil
	} else if !isMissing(err) {
		return "", err
	}
	tail := ""
	current := absolute
	for {
		// os.Open 把悬空链接报告为"缺失"，因此没有这个检查时，循环会重新挂接
		// 链接的字面名称，从而隐藏它指向别处（可能指向写入 root 之外）的事实。
		// 读取链接本身即使在目标尚不存在时也能解析它，这正是边界检查所需的
		// 先写后创建场景。
		if target, ok := linkTarget(current); ok {
			reattached := target
			if tail != "" {
				reattached = filepath.Join(target, tail)
			}
			return resolveWithTail(filepath.Clean(reattached))
		}
		base := filepath.Base(current)
		if tail == "" {
			tail = base
		} else {
			tail = filepath.Join(base, tail)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("security: no resolvable ancestor for %q", absolute)
		}
		current = parent
		if resolved, err := realPathExisting(current); err == nil {
			return filepath.Join(resolved, tail), nil
		} else if !isMissing(err) {
			return "", err
		}
	}
}

// linkTarget 返回 path 处符号链接或 Windows junction 的解析后目标。与
// realPathExisting 不同，它读取链接对象本身（Lstat + Readlink），而不是穿过它
// 打开，因此也适用于目标尚不存在的悬空链接。对于普通路径以及链接无法读取时，
// ok 为 false；此时调用方回退到字面遍历。
func linkTarget(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !isLinkInfo(info) {
		return "", false
	}
	target, err := os.Readlink(path)
	if err != nil || target == "" {
		return "", false
	}
	target = normalizeLinkTarget(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target), true
}
