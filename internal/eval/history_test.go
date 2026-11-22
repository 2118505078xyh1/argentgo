package eval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeGit 在测试期间替换 git 调用入口，返回调用记录供断言。
func withFakeGit(t *testing.T, fn func(ctx context.Context, dir string, args ...string) ([]byte, error)) {
	t.Helper()
	orig := historyGitCommand
	historyGitCommand = fn
	t.Cleanup(func() { historyGitCommand = orig })
}

func TestDiscoverHistoryTasksParsesCommits(t *testing.T) {
	withFakeGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "log":
			return []byte("abcdef1234567890\x1ffix: the bug\n" +
				"9999999999999999\x1ffeat: add thing\n"), nil
		case "rev-parse":
			if strings.Contains(args[1], "9999") {
				return nil, os.ErrNotExist // root 提交，跳过
			}
			return []byte("parentsha\n"), nil
		case "diff-tree":
			return []byte("pkg/foo_test.go\nREADME.md\n"), nil // 只有 _test.go 入选
		case "show":
			return []byte("package pkg\nfunc TestFoo() {}\n"), nil
		}
		return nil, nil
	})

	set, err := DiscoverHistoryTasks(context.Background(), "/repo", HistoryOptions{MaxTasks: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1 (root skipped, non-test file ignored)", len(set.Scenarios))
	}
	sc := set.Scenarios[0]
	if sc.Prompt != "fix: the bug" {
		t.Fatalf("prompt = %q, want commit message", sc.Prompt)
	}
	if got := sc.Files["pkg/foo_test.go"]; got != "package pkg\nfunc TestFoo() {}\n" {
		t.Fatalf("file content = %q", got)
	}
	if !sc.Check.AllowExec || sc.Check.Exec == nil || sc.Check.Exec.Command != "go" {
		t.Fatal("check must allow go test exec")
	}
	if sc.Check.FileEquals["pkg/foo_test.go"] != sc.Files["pkg/foo_test.go"] {
		t.Fatal("test file must be byte-locked to prevent cheating")
	}
	if set.parents[sc.Name] != "parentsha" {
		t.Fatalf("parent = %q, want parentsha", set.parents[sc.Name])
	}
}

func TestSafeHistoryPath(t *testing.T) {
	good := []string{"foo_test.go", "pkg/foo_test.go", "a/b/c_test.go"}
	bad := []string{"", "../evil_test.go", "/abs/foo_test.go", `..\evil_test.go`, "a/../../x_test.go"}
	for _, p := range good {
		if !safeHistoryPath(p) {
			t.Errorf("safeHistoryPath(%q) = false, want true", p)
		}
	}
	for _, p := range bad {
		if safeHistoryPath(p) {
			t.Errorf("safeHistoryPath(%q) = true, want false", p)
		}
	}
}

// initTestRepo 建一个含两个提交的临时 git 仓库：第一个只有源码，第二个新增测试文件。
func initTestRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/t\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "lib.go"), []byte("package t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "first: scaffold")
	// 第二次提交同时实现 Add 并新增测试：父提交 + 测试会因 undefined: Add 失败，
	// 提交本身 + 测试通过，这才构成有判别力的任务。
	if err := os.WriteFile(filepath.Join(repo, "lib.go"), []byte("package t\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "lib_test.go"), []byte("package t\nimport \"testing\"\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"bad\") } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "second: add a test for the bug")
	return repo
}

// TestDiscoverHistoryTasksRealRepo 用真实 git 仓库验证端到端抽取。
func TestDiscoverHistoryTasksRealRepo(t *testing.T) {
	repo := initTestRepo(t)
	set, err := DiscoverHistoryTasks(context.Background(), repo, HistoryOptions{MaxTasks: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1", len(set.Scenarios))
	}
	sc := set.Scenarios[0]
	if sc.Prompt != "second: add a test for the bug" {
		t.Fatalf("prompt = %q", sc.Prompt)
	}
	if _, ok := sc.Files["lib_test.go"]; !ok {
		t.Fatalf("files = %v, want lib_test.go", sc.Files)
	}
}

// TestHistoryWorktreeFactory 验证 worktree 隔离：建树不动真实工作树，
// cleanup 后目录消失且 git worktree list 不含该项。
func TestHistoryWorktreeFactory(t *testing.T) {
	repo := initTestRepo(t)
	set, err := DiscoverHistoryTasks(context.Background(), repo, HistoryOptions{MaxTasks: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Scenarios) == 0 {
		t.Fatal("no scenarios discovered")
	}

	dir, cleanup, err := set.WorktreeFactory(repo)(set.Scenarios[0])
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == nil {
		t.Fatal("nil cleanup")
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("worktree missing go.mod: %v", err)
	}
	// 父提交里还没有测试文件，验证 worktree 指向的是父提交而非真实工作树。
	if _, err := os.Stat(filepath.Join(dir, "lib_test.go")); !os.IsNotExist(err) {
		t.Fatalf("worktree should be at parent commit (no test file), stat err = %v", err)
	}
	// 真实工作树仍然干净。
	status, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(status)) != "" {
		t.Fatalf("real worktree dirty: %s", status)
	}

	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still exists after cleanup: %v", err)
	}
	list, err := exec.Command("git", "-C", repo, "worktree", "list").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(list), dir) {
		t.Fatalf("worktree still registered after cleanup: %s", list)
	}
}

// TestVerifyHistoryTasks 用真实 git 仓库验证任务自检：新增的测试在父提交下
// 必须失败（因为源码还没实现），在提交本身下必须通过（参考解成立）。
func TestVerifyHistoryTasks(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	repo := initTestRepo(t)
	results, err := VerifyHistoryTasks(context.Background(), repo, HistoryOptions{MaxTasks: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if !results[0].Valid() {
		t.Fatalf("history task failed self-check: %+v", results[0])
	}
}
