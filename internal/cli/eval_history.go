package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/eval"
)

// runEvalHistory 实现 `eval history <repo> [--max-tasks N] [--since REF]`：
// 从 git 历史抽取任务，在隔离 worktree 中用真实模型执行并评分。
// 显式子命令本身即 opt-in，不额外引入 ARGENT_* 环境门控。
func runEvalHistory(app App, ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		evalHistoryUsage(app.IO.Out)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(app.IO.Err, "eval history: repository path is required")
		evalHistoryUsage(app.IO.Err)
		return 2
	}

	spec := HistoryEvalSpec{Repo: strings.TrimSpace(args[0])}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--max-tasks" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				fmt.Fprintf(app.IO.Err, "eval history: invalid --max-tasks %q\n", args[i])
				return 2
			}
			spec.MaxTasks = n
		case strings.HasPrefix(arg, "--max-tasks="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--max-tasks="))
			if err != nil || n <= 0 {
				fmt.Fprintf(app.IO.Err, "eval history: invalid --max-tasks %q\n", arg)
				return 2
			}
			spec.MaxTasks = n
		case arg == "--since" && i+1 < len(args):
			i++
			spec.Since = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--since="):
			spec.Since = strings.TrimSpace(strings.TrimPrefix(arg, "--since="))
		case arg == "--scripted":
			spec.Scripted = true
		default:
			fmt.Fprintf(app.IO.Err, "eval history: unknown argument %q\n", arg)
			evalHistoryUsage(app.IO.Err)
			return 2
		}
	}

	runner, ok := app.Eval.(HistoryEvalSuiteRunner)
	if !ok {
		fmt.Fprintln(app.IO.Err, "eval history: the current evaluation backend does not support history runs")
		return 2
	}
	passed, failed, err := runner.RunHistory(ctx, spec, app.IO.Out)
	if err != nil {
		fmt.Fprintf(app.IO.Err, "eval history: %v\n", err)
		return 1
	}
	if failed > 0 {
		return 1
	}
	_ = passed // 报告已写入 out；退出码只需 failed 判定。
	return 0
}

// runEvalHistoryVerify 离线验证历史任务的判别力：每个任务必须在父提交下失败、
// 在参考提交下通过。直接调用 eval 包的纯函数，不依赖 app/API key。
func runEvalHistoryVerify(app App, args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(app.IO.Out, `usage: argentgo eval history verify <repo> [--max-tasks <n>] [--since <ref>]

Checks every extracted task for discriminating power: the added test must fail
on the parent commit and pass on the commit itself. Offline, no model needed.
`)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(app.IO.Err, "eval history verify: repository path is required")
		return 2
	}

	repo := strings.TrimSpace(args[0])
	maxTasks, since := 0, ""
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--max-tasks" && i+1 < len(args):
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				fmt.Fprintf(app.IO.Err, "eval history verify: invalid --max-tasks %q\n", args[i])
				return 2
			}
			maxTasks = n
		case strings.HasPrefix(arg, "--max-tasks="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--max-tasks="))
			if err != nil || n <= 0 {
				fmt.Fprintf(app.IO.Err, "eval history verify: invalid --max-tasks %q\n", arg)
				return 2
			}
			maxTasks = n
		case arg == "--since" && i+1 < len(args):
			i++
			since = strings.TrimSpace(args[i])
		case strings.HasPrefix(arg, "--since="):
			since = strings.TrimSpace(strings.TrimPrefix(arg, "--since="))
		default:
			fmt.Fprintf(app.IO.Err, "eval history verify: unknown argument %q\n", arg)
			return 2
		}
	}

	results, err := eval.VerifyHistoryTasks(context.Background(), repo, eval.HistoryOptions{
		MaxTasks: maxTasks,
		Since:    since,
	})
	if err != nil {
		fmt.Fprintf(app.IO.Err, "eval history verify: %v\n", err)
		return 1
	}
	invalid := 0
	for _, r := range results {
		status := "OK"
		if !r.Valid() {
			status = "INVALID"
			invalid++
		}
		fmt.Fprintf(app.IO.Out, "%s %s (parentFailed=%v commitPassed=%v%s)\n",
			status, r.Name, r.ParentFailed, r.CommitPassed,
			func() string {
				if r.Err != "" {
					return " err=" + r.Err
				}
				return ""
			}())
	}
	if invalid > 0 {
		fmt.Fprintf(app.IO.Out, "%d/%d history tasks failed self-check\n", invalid, len(results))
		return 1
	}
	fmt.Fprintf(app.IO.Out, "%d/%d history tasks passed self-check\n", len(results), len(results))
	return 0
}

func evalHistoryUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo eval history <repo> [--max-tasks <n>] [--since <ref>] [--scripted]

Extracts recent commits that added test files from <repo>. Each commit's
message becomes the prompt; its added test file is seeded into a temporary
git worktree at the parent commit, so the real working tree is never touched.
The real model is required (flag/config/login/provider env). Default: 15 tasks.
With --scripted, a one-turn scripted reply replaces the real model so the
worktree/grading flow can be checked offline (tasks usually fail by design).
`)
}
