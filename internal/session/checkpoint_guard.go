package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RestoreStatus 是一次检查点还原前置校验的显式结果枚举。
// 用字符串枚举而非布尔值，是为了让调用方穷尽处理每一种结局，
// 从而杜绝"校验未通过却继续还原"造成的半恢复状态。
type RestoreStatus string

const (
	// RestoreOK 表示全部校验通过，工作区可以安全地进行还原。
	RestoreOK RestoreStatus = "restored"
	// RestoreSnapshotsDisabled 表示快照功能被显式禁用。
	RestoreSnapshotsDisabled RestoreStatus = "snapshots_disabled"
	// RestoreUnavailable 表示快照子系统不可用（基线目录缺失、非目录或不可写）。
	RestoreUnavailable RestoreStatus = "unavailable"
	// RestoreNotAGitRepo 表示 baseDir 不在 git 仓库内。
	RestoreNotAGitRepo RestoreStatus = "not_a_git_repo"
	// RestoreValidationFailed 表示目标路径校验失败（逃逸、越界、不存在等）。
	RestoreValidationFailed RestoreStatus = "validation_failed"
)

// CheckpointGuard 在改动工作区之前对还原目标做只读前置校验。
// 它不创建、修改或删除任何文件，只读取路径与元数据并返回结构化结论。
// 零值不可用（SnapshotsEnabled 默认为 false），调用方应显式配置。
type CheckpointGuard struct {
	// SnapshotsEnabled 为 false 时，Validate 直接返回 RestoreSnapshotsDisabled。
	SnapshotsEnabled bool
	// BaselineDir 是还原前保存基线的目录。非空时要求其存在、为目录且具有写权限；
	// 不满足时返回 RestoreUnavailable。为空则跳过该检查。
	BaselineDir string
	// RequireGitRepo 要求 baseDir 位于 git 仓库内；否则返回 RestoreNotAGitRepo。
	RequireGitRepo bool
	// Problems 是外部预先发现的问题，会被复制到每次 Validate 的问题列表中。
	// 它只被读取，Validate 绝不修改它。
	Problems []string
}

// Validate 只读校验 target 能否作为还原目标，返回状态与问题描述列表。
// 返回的问题是 g.Problems 的副本与本次校验新发现问题的合集；
// 调用方修改返回切片不会影响 guard 的内部状态。本函数不写任何文件。
func (g *CheckpointGuard) Validate(baseDir, target string) (RestoreStatus, []string) {
	// 防御性复制：绝不把内部切片暴露给调用方，也不修改 g.Problems。
	problems := append([]string(nil), g.Problems...)

	if !g.SnapshotsEnabled {
		problems = append(problems, "快照功能未启用")
		return RestoreSnapshotsDisabled, problems
	}
	if g.RequireGitRepo && !guardIsGitRepo(baseDir) {
		problems = append(problems, fmt.Sprintf("baseDir 不在 git 仓库内: %s", baseDir))
		return RestoreNotAGitRepo, problems
	}
	if g.BaselineDir != "" && !guardBaselineWritable(g.BaselineDir, &problems) {
		return RestoreUnavailable, problems
	}
	if !guardTargetWithin(baseDir, target, &problems) {
		return RestoreValidationFailed, problems
	}
	return RestoreOK, problems
}

// guardIsGitRepo 只读判断 dir 或其任一祖先是否包含 .git（目录或文件）。
func guardIsGitRepo(dir string) bool {
	cur := strings.TrimSpace(dir)
	if cur == "" {
		return false
	}
	cur, err := filepath.Abs(cur)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		cur = parent
	}
}

// guardBaselineWritable 只读检查基线目录是否存在、为目录且具有写权限。
// 它不创建探测文件，严格遵守"只校验不写文件"的约束。
func guardBaselineWritable(dir string, problems *[]string) bool {
	clean := strings.TrimSpace(dir)
	if clean == "" {
		*problems = append(*problems, "基线目录为空")
		return false
	}
	info, err := os.Stat(clean)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("基线目录不可访问: %s: %v", clean, err))
		return false
	}
	if !info.IsDir() {
		*problems = append(*problems, fmt.Sprintf("基线路径不是目录: %s", clean))
		return false
	}
	if info.Mode().Perm()&0o200 == 0 {
		*problems = append(*problems, fmt.Sprintf("基线目录不可写: %s", clean))
		return false
	}
	return true
}

// guardTargetWithin 只读校验 target 是否位于 baseDir 内且真实存在。
// 它拒绝空路径、NUL 字符、越界的 .. 段，以及通过符号链接指向 baseDir 之外的目标。
// CJK 路径按 UTF-8 字节原样处理，不做字节级裁剪。全程不写任何文件。
func guardTargetWithin(baseDir, target string, problems *[]string) bool {
	root := strings.TrimSpace(baseDir)
	if root == "" {
		*problems = append(*problems, "baseDir 为空")
		return false
	}
	root, err := filepath.Abs(root)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("无法解析 baseDir: %v", err))
		return false
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	raw := strings.TrimSpace(target)
	if raw == "" {
		*problems = append(*problems, "target 为空")
		return false
	}
	if strings.ContainsRune(raw, 0) {
		*problems = append(*problems, "target 含有 NUL 字符")
		return false
	}

	var candidate string
	if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" ||
		strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
		// 绝对路径：直接规范化，稍后统一做 baseDir 包含性检查。
		candidate = filepath.Clean(filepath.FromSlash(raw))
	} else {
		clean := filepath.Clean(filepath.FromSlash(raw))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			*problems = append(*problems, fmt.Sprintf("target 逃逸 baseDir: %q", raw))
			return false
		}
		candidate = filepath.Join(root, clean)
	}

	// Windows 的临时目录（如 CI runner 的 C:\Users\RUNNER~1\...）可能是 8.3
	// 短路径，而 root 经 EvalSymlinks 后是长路径；若直接用短路径 candidate 与
	// 长路径 root 做 filepath.Rel，会被误判为逃逸。先把存在的 candidate 也解析
	// 为规范长路径，再统一比较。Lstat 不存在的情况留给下面的存在性检查处理。
	if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = resolved
	}

	if !guardWithinRoot(root, candidate) {
		*problems = append(*problems, fmt.Sprintf("target 逃逸 baseDir: %q", raw))
		return false
	}

	// 目标必须真实存在；不存在时视为校验失败。
	if _, err := os.Lstat(candidate); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			*problems = append(*problems, fmt.Sprintf("target 不存在: %q", raw))
		} else {
			*problems = append(*problems, fmt.Sprintf("无法访问 target: %q: %v", raw, err))
		}
		return false
	}

	// 解析符号链接后再次确认仍位于 baseDir 内，防止借符号链接逃逸。
	resolvedTarget, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		*problems = append(*problems, fmt.Sprintf("无法解析 target 符号链接: %q: %v", raw, err))
		return false
	}
	if !guardWithinRoot(root, resolvedTarget) {
		*problems = append(*problems, fmt.Sprintf("target 经符号链接逃逸 baseDir: %q", raw))
		return false
	}
	return true
}

// guardWithinRoot 判断 candidate 是否位于 root 之内（含 root 本身）。
func guardWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
