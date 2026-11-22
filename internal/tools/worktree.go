package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// 本文件实现 git worktree 的三个工具：worktree_create / worktree_list /
// worktree_remove。它们刻意不进默认注册表（golden 逐字节不变），由 app
// 显式调用 RegisterWorktrees 注册。
//
// 安全边界：worktree 只能创建在 root 下的固定目录 .argentgo-worktrees/ 内，
// 名字经严格校验（防路径逃逸与分支名注入），remove 只能移除该目录内的条目。
// 所有 git 调用都用 exec.CommandContext + 参数数组（不经过 shell，杜绝注入），
// 并带超时。

const (
	worktreeDirName      = ".argentgo-worktrees"
	worktreeMaxNameRunes = 63
	worktreeMaxListBytes = 32 << 10
)

// worktreeNameRE 约束 worktree 名同时作为目录名与 git 分支名：
// 以字母/数字开头，只含字母数字、点、下划线、连字符、斜杠，长度有界。
// 这排除了 .. 逃逸、前导 -（参数注入）、空格与 shell 元字符。
var worktreeNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}$`)

// 可被测试替换的 git 调用工厂，与 gitStatusCommand 同范式。
var gitWorktreeCommand = func(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd
}

// validWorktreeDir 返回 root 下受控的 worktree 父目录，并确认它始终在 root 内。
func validWorktreeDir(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("worktree: resolve root: %w", err)
	}
	return filepath.Join(absRoot, worktreeDirName), nil
}

// validateWorktreeName 校验名字并返回其在受控目录下的绝对路径。
func validateWorktreeName(root, name string) (string, string, error) {
	if name == "" {
		return "", "", fmt.Errorf("worktree: name must not be empty")
	}
	if len(name) > worktreeMaxNameRunes || !worktreeNameRE.MatchString(name) {
		return "", "", fmt.Errorf("worktree: invalid name %q (use letters/digits/._/- starting with a letter or digit, max %d chars)", name, worktreeMaxNameRunes)
	}
	if strings.Contains(name, "..") {
		return "", "", fmt.Errorf("worktree: name must not contain '..'")
	}
	parent, err := validWorktreeDir(root)
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(parent, name)
	// 双保险：Clean 后必须仍在 parent 之下，防斜杠组合逃逸。
	rel, err := filepath.Rel(parent, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("worktree: name escapes the worktree directory")
	}
	return path, parent, nil
}

func runWorktreeGit(ctx context.Context, dir string, args ...string) (string, error) {
	timeout := clampTimeout("worktree", 0, maxTimeout(ctx))
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := gitWorktreeCommand(runCtx, dir, args...).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("worktree: git timed out after %s", timeout)
	}
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("worktree: %w", err)
		}
		return "", fmt.Errorf("worktree: %s: %w", text, err)
	}
	return text, nil
}

// ---- worktree_create ----

type worktreeCreateTool struct{}

func (worktreeCreateTool) Name() string { return "worktree_create" }
func (worktreeCreateTool) Description() string {
	return "Create an isolated git worktree in .argentgo-worktrees/<name> with a new branch of the same name. Returns its absolute path. Use it to make changes without touching the current working tree."
}
func (worktreeCreateTool) Mutating() bool { return true }
func (worktreeCreateTool) Schema() string {
	return `{"type":"object","properties":{"name":{"type":"string","description":"worktree/branch name: letters, digits, ., _, -, /; must start with a letter or digit"}},"required":["name"]}`
}

func (worktreeCreateTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("worktree_create: bad arguments: %w", err)
	}
	path, parent, err := validateWorktreeName(root, args.Name)
	if err != nil {
		return "", err
	}
	// git worktree add 需要父目录存在。
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("worktree_create: %w", err)
	}
	// 参数数组传递，name 已严格校验；-b 创建新分支。
	if _, err := runWorktreeGit(ctx, root, "worktree", "add", "-b", args.Name, path); err != nil {
		return "", err
	}
	return fmt.Sprintf("worktree %q created at %s (branch %s)", args.Name, path, args.Name), nil
}

// ---- worktree_list ----

type worktreeListTool struct{}

func (worktreeListTool) Name() string { return "worktree_list" }
func (worktreeListTool) Description() string {
	return "List git worktrees of the current repository (git worktree list). Read-only."
}
func (worktreeListTool) Mutating() bool { return false }
func (worktreeListTool) Schema() string { return `{"type":"object","properties":{}}` }

func (worktreeListTool) Run(ctx context.Context, root string, _ json.RawMessage) (string, error) {
	text, err := runWorktreeGit(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	if text == "" {
		return "(no worktrees)", nil
	}
	if len(text) > worktreeMaxListBytes {
		text = text[:worktreeMaxListBytes] + "\n… [truncated]"
	}
	return text, nil
}

// ---- worktree_remove ----

type worktreeRemoveTool struct{}

func (worktreeRemoveTool) Name() string { return "worktree_remove" }
func (worktreeRemoveTool) Description() string {
	return "Remove a worktree previously created under .argentgo-worktrees/<name>. Only names in that controlled directory can be removed."
}
func (worktreeRemoveTool) Mutating() bool { return true }
func (worktreeRemoveTool) Schema() string {
	return `{"type":"object","properties":{"name":{"type":"string","description":"name of the worktree to remove"}},"required":["name"]}`
}

func (worktreeRemoveTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("worktree_remove: bad arguments: %w", err)
	}
	path, _, err := validateWorktreeName(root, args.Name)
	if err != nil {
		return "", err
	}
	if _, err := runWorktreeGit(ctx, root, "worktree", "remove", path); err != nil {
		return "", err
	}
	return fmt.Sprintf("worktree %q removed", args.Name), nil
}

// RegisterWorktrees 把三个 worktree 工具显式追加到 r 的末尾（幂等）。
// 绝不被 New/NewWithLimit 调用，默认 Specs()/Names() 与 golden 逐字节不变。
func RegisterWorktrees(r *Registry) {
	if r == nil {
		return
	}
	for _, tool := range []Tool{worktreeCreateTool{}, worktreeListTool{}, worktreeRemoveTool{}} {
		if _, exists := r.tools[tool.Name()]; exists {
			continue
		}
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
}
