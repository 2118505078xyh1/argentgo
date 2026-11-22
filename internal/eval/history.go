package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 本文件实现 killi 决策 15 的"从 git 历史抽取 eval 任务"：
// 取近期提交的 message 当 prompt，把该提交新增的测试文件种入父提交的工作树，
// 用该测试评分。整个过程在临时 git worktree 中进行，绝不触碰真实工作树。
// 这是纯新增的 opt-in 路径，默认 eval 行为逐字节不变。

const (
	historyDefaultMaxTasks = 15
	historyMaxMessageLen   = 500 // commit message 过长时截断，避免撑爆 prompt
	historyGitTimeout      = 30 * time.Second
)

// historyGitCommand 是 git 调用的可替换入口，便于测试注入假实现。
// 参数以 argv 数组传递，绝不经过 shell；dir 为空时用当前目录。
var historyGitCommand = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, historyGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// HistoryOptions 配置从 git 历史抽取任务。
type HistoryOptions struct {
	// MaxTasks 是最多抽取的提交数（跳过 merge 与 root），<=0 取默认值 15。
	MaxTasks int
	// Since 是可选的 git revision 范围起点；为空时取最近 MaxTasks 个提交。
	Since string
	// TestPath 是新增测试文件的匹配后缀，默认 "_test.go"。
	TestPath string
	// CheckTimeoutMS 是 go test 检查的超时（毫秒）；<=0 取 120000。
	// 默认 30s 对全新 worktree 的首次编译太短，容易误判为任务失败。
	CheckTimeoutMS int
}

// HistoryTaskSet 是一次抽取的结果。Scenarios 交给 eval.Run，parents 记录每个
// 场景对应的父提交，供 WorktreeFactory 建隔离工作树。
type HistoryTaskSet struct {
	Scenarios []Scenario
	parents   map[string]string
	commits   map[string]string
}

// safeHistoryPath 拒绝绝对路径与 .. 逃逸，避免恶意提交里的文件名越出工作区。
func safeHistoryPath(p string) bool {
	if p == "" || filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return false
	}
	clean := path.Clean(filepath.ToSlash(p))
	return clean != "." && clean != ".." &&
		!strings.HasPrefix(clean, "../") && !strings.HasPrefix(clean, `../`)
}

// DiscoverHistoryTasks 从仓库近期提交抽取任务。
// 只处理"新增了测试文件"的提交；merge 与 root 提交跳过。
func DiscoverHistoryTasks(ctx context.Context, repo string, opts HistoryOptions) (*HistoryTaskSet, error) {
	maxTasks := opts.MaxTasks
	if maxTasks <= 0 {
		maxTasks = historyDefaultMaxTasks
	}
	testPath := opts.TestPath
	if testPath == "" {
		testPath = "_test.go"
	}
	checkTimeout := opts.CheckTimeoutMS
	if checkTimeout <= 0 {
		checkTimeout = 120000
	}

	logArgs := []string{"log", "--no-merges", "--format=%H%x1f%s", "-n", fmt.Sprintf("%d", maxTasks)}
	if opts.Since != "" {
		logArgs = append(logArgs, opts.Since+"..HEAD")
	}
	out, err := historyGitCommand(ctx, repo, logArgs...)
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	set := &HistoryTaskSet{parents: make(map[string]string), commits: make(map[string]string)}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 2)
		if len(parts) != 2 {
			continue
		}
		sha, message := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if sha == "" {
			continue
		}

		parentOut, err := historyGitCommand(ctx, repo, "rev-parse", sha+"^1")
		if err != nil {
			continue // root 提交没有父，跳过
		}
		parent := strings.TrimSpace(string(parentOut))

		addedOut, err := historyGitCommand(ctx, repo, "diff-tree",
			"--no-commit-id", "--name-only", "-r", "--diff-filter=A", "--no-renames", sha)
		if err != nil {
			continue
		}
		files := make(map[string]string)
		for _, raw := range strings.Split(strings.TrimSpace(string(addedOut)), "\n") {
			name := strings.TrimSpace(raw)
			if name == "" || !strings.HasSuffix(name, testPath) || !safeHistoryPath(name) {
				continue
			}
			content, err := historyGitCommand(ctx, repo, "show", sha+":"+name)
			if err != nil {
				continue
			}
			files[name] = string(content)
		}
		if len(files) == 0 {
			continue // 没有新增测试文件的提交不构成任务
		}

		// 只跑新增测试文件所在的包，而不是整个仓库：全新 worktree 里
		// `go test ./...` 首次编译可能超过 120s，而单包通常几秒完成。
		pkgSet := make(map[string]bool)
		for name := range files {
			dir := filepath.Dir(filepath.ToSlash(name))
			if dir == "." {
				dir = "./"
			} else {
				dir = "./" + dir
			}
			pkgSet[dir] = true
		}
		pkgs := make([]string, 0, len(pkgSet))
		for p := range pkgSet {
			pkgs = append(pkgs, p)
		}
		sort.Strings(pkgs)
		execArgs := append([]string{"test"}, pkgs...)

		if len(message) > historyMaxMessageLen {
			message = message[:historyMaxMessageLen] + "…"
		}
		name := fmt.Sprintf("history %s", shortHistorySHA(sha))
		set.Scenarios = append(set.Scenarios, Scenario{
			Name:   name,
			Prompt: message,
			Files:  files,
			// 锁住测试文件字节，防止模型直接改测试刷分（killi 的 check 是测试通过）。
			Check: Check{
				FileEquals: files,
				AllowExec:  true,
				Exec:       &ExecCheck{Command: "go", Args: execArgs, TimeoutMS: checkTimeout},
			},
		})
		set.parents[name] = parent
		set.commits[name] = sha
		if len(set.Scenarios) >= maxTasks {
			break
		}
	}
	return set, nil
}

func shortHistorySHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// worktreeAt 在任意提交的 --detach worktree 中创建隔离目录，返回清理函数。
func worktreeAt(repo, commit string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "argent-hist-")
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(tmp, "tree")
	if _, err := historyGitCommand(context.Background(), repo,
		"worktree", "add", "--detach", dir, commit); err != nil {
		_ = os.RemoveAll(tmp)
		return "", nil, fmt.Errorf("git worktree add: %w", err)
	}
	cleanup := func() {
		// worktree 先移除再删目录，最后 prune 清掉 .git/worktrees 元数据；
		// 任何一步失败都不影响后续步骤，避免残留污染真实仓库。
		_, _ = historyGitCommand(context.Background(), repo, "worktree", "remove", "--force", dir)
		_, _ = historyGitCommand(context.Background(), repo, "worktree", "prune")
		_ = os.RemoveAll(tmp)
	}
	return dir, cleanup, nil
}

// WorktreeFactory 返回一个供 eval.Options.WorkspaceFactory 使用的工厂：
// 在父提交的 --detach worktree 中种入新增测试文件，评分后彻底清理 worktree。
func (s *HistoryTaskSet) WorktreeFactory(repo string) func(Scenario) (string, func(), error) {
	return func(sc Scenario) (string, func(), error) {
		parent, ok := s.parents[sc.Name]
		if !ok || parent == "" {
			return "", nil, fmt.Errorf("no parent commit recorded for %q", sc.Name)
		}
		return worktreeAt(repo, parent)
	}
}

// HistoryVerifyResult 是一个历史任务的自检结果。
// killi 决策 15 要求：每个任务在"原样"下必须失败、在参考解（该提交）下必须通过，
// 否则任务本身没有判别力，不应进入评测集。
type HistoryVerifyResult struct {
	Name         string
	ParentFailed bool // 父提交 + 新测试 → go test 失败（原样不成立）
	CommitPassed bool // 提交本身 + 测试 → go test 通过（参考解成立）
	Err          string
}

// Valid 报告该任务是否通过自检。
func (r HistoryVerifyResult) Valid() bool {
	return r.Err == "" && r.ParentFailed && r.CommitPassed
}

// VerifyHistoryTasks 离线验证每个历史任务的判别力：
// 在父提交 worktree 种入新增测试并跑 go test（期望失败），
// 在该提交 worktree 跑同一测试（期望通过）。全程不联网、不调用模型。
func VerifyHistoryTasks(ctx context.Context, repo string, opts HistoryOptions) ([]HistoryVerifyResult, error) {
	set, err := DiscoverHistoryTasks(ctx, repo, opts)
	if err != nil {
		return nil, err
	}
	return VerifyHistoryTaskSet(repo, set)
}

// VerifyHistoryTaskSet 是 VerifyHistoryTasks 的"已抽取结果"版本，
// 避免调用方已经 Discover 过一遍时重复调用 git。
func VerifyHistoryTaskSet(repo string, set *HistoryTaskSet) ([]HistoryVerifyResult, error) {
	if set == nil {
		return nil, errors.New("nil history task set")
	}
	results := make([]HistoryVerifyResult, 0, len(set.Scenarios))
	for _, sc := range set.Scenarios {
		check := ExecCheck{}
		if sc.Check.Exec != nil {
			check = *sc.Check.Exec
		} else {
			check = ExecCheck{Command: "go", Args: []string{"test", "./..."}}
		}
		result := HistoryVerifyResult{Name: sc.Name}
		parentDir, parentCleanup, err := worktreeAt(repo, set.parents[sc.Name])
		if err != nil {
			result.Err = err.Error()
			results = append(results, result)
			continue
		}
		if err := seed(parentDir, sc.Files); err != nil {
			result.Err = err.Error()
			parentCleanup()
			results = append(results, result)
			continue
		}
		parentRes := RunExecCheck(parentDir, check)
		parentCleanup()
		result.ParentFailed = parentRes.ExitCode != 0

		commitDir, commitCleanup, err := worktreeAt(repo, set.commits[sc.Name])
		if err != nil {
			result.Err = err.Error()
			results = append(results, result)
			continue
		}
		commitRes := RunExecCheck(commitDir, check)
		commitCleanup()
		result.CommitPassed = commitRes.ExitCode == 0
		results = append(results, result)
	}
	return results, nil
}
