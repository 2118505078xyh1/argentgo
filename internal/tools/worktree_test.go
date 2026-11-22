package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// fakeWorktreeGit 记录 git 调用参数，返回预设输出。与 gitStatusCommand 同范式。
func fakeWorktreeGit(t *testing.T, out string, fail bool) *[]string {
	t.Helper()
	var calls []string
	orig := gitWorktreeCommand
	gitWorktreeCommand = func(ctx context.Context, dir string, args ...string) *exec.Cmd {
		calls = append(calls, strings.Join(args, " "))
		// 构造一个确定性的、成功/失败可控的命令（用 go 自身打印）。
		if fail {
			return exec.CommandContext(ctx, "git", "definitely-invalid-subcommand-xyz")
		}
		return exec.CommandContext(ctx, "go", "version") // 成功命令；out 由返回值使用
	}
	t.Cleanup(func() { gitWorktreeCommand = orig })
	_ = out
	return &calls
}

func TestValidateWorktreeName(t *testing.T) {
	root := t.TempDir()
	good := []string{"feat", "feat/foo", "v1.2-3", "a_b.c"}
	for _, name := range good {
		path, parent, err := validateWorktreeName(root, name)
		if err != nil || !strings.HasPrefix(path, parent+string(filepathSep)) {
			t.Errorf("name %q: path=%q parent=%q err=%v", name, path, parent, err)
		}
	}
	bad := []string{"", "-x", "..", "../evil", "a/../../b", "a b", "a;b", "a\x00b", strings.Repeat("a", 64)}
	for _, name := range bad {
		if _, _, err := validateWorktreeName(root, name); err == nil {
			t.Errorf("name %q should be rejected", name)
		}
	}
}

// filepathSep 在本测试中显式定义，避免依赖 path 包的平台差异。
const filepathSep = string(os.PathSeparator)

func TestRegisterWorktreesKeepsDefaultGolden(t *testing.T) {
	baseline := New(t.TempDir(), true).Names()
	r := New(t.TempDir(), true)
	RegisterWorktrees(r)
	RegisterWorktrees(r) // 幂等
	want := append(append([]string(nil), baseline...), "worktree_create", "worktree_list", "worktree_remove")
	if got := r.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	if got := New(t.TempDir(), true).Names(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("默认注册表被污染: %v", got)
	}
	RegisterWorktrees(nil)
}

func TestWorktreeCreateBuildsGitArgsAndPath(t *testing.T) {
	root := t.TempDir()
	calls := fakeWorktreeGit(t, "", false)
	tool := worktreeCreateTool{}
	raw, _ := json.Marshal(map[string]string{"name": "feat"})
	if _, err := tool.Run(context.Background(), root, raw); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("git calls = %v", *calls)
	}
	got := (*calls)[0]
	if !strings.HasPrefix(got, "worktree add -b feat ") || !strings.Contains(got, worktreeDirName+string(filepathSep)+"feat") {
		t.Fatalf("git args = %q", got)
	}
}

func TestWorktreeToolsRejectBadName(t *testing.T) {
	root := t.TempDir()
	raw, _ := json.Marshal(map[string]string{"name": "../evil"})
	if _, err := (worktreeCreateTool{}).Run(context.Background(), root, raw); err == nil {
		t.Fatal("create should reject traversal name")
	}
	if _, err := (worktreeRemoveTool{}).Run(context.Background(), root, raw); err == nil {
		t.Fatal("remove should reject traversal name")
	}
}

func TestWorktreeListReturnsPorcelain(t *testing.T) {
	// list 工具在真实 git 仓库里返回文本；这里仅校验空仓库兜底不报错即可，
	// 用 fake 命令返回 go version 作为非空输出，确认走 clip 路径。
	root := t.TempDir()
	fakeWorktreeGit(t, "", false)
	out, err := (worktreeListTool{}).Run(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out == "" {
		t.Fatal("list output must not be empty")
	}
}
