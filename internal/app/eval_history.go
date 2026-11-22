package app

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/eval"
)

// 本文件实现 eval history 的 app 层接线：用真实模型执行从 git 历史抽取的任务，
// 每个任务在自己的临时 worktree 中运行（见 internal/eval/history.go）。
// 它与 RunSuiteLive 同构，但输入是仓库路径与抽取选项，而不是场景文档。

var _ cli.HistoryEvalSuiteRunner = (*evaler)(nil)

// RunHistory 从 git 历史抽取任务并用真实模型执行。
// 凭据经 Resolve 取得（flag/config/login/provider env），无 key 时明确报错；
// API key 只在内存中流向 provider，报告路径不会写入 out。
func (e *evaler) RunHistory(ctx context.Context, spec cli.HistoryEvalSpec, out io.Writer) (int, int, error) {
	if strings.TrimSpace(spec.Repo) == "" {
		return 0, 0, errors.New("eval history: repository path is required")
	}

	resolved, err := Resolve(e.stdio, cli.Options{Cwd: e.cwd})
	if err != nil {
		return 0, 0, errors.New("eval history: resolve failed")
	}
	if strings.TrimSpace(resolved.APIKey) == "" {
		return 0, 0, errors.New("eval history requires an API key (flag/config/login/provider env)")
	}

	// 与 RunSuiteLive 相同的协议白名单校验，防止把凭据发往不可信协议。
	liveCfg, readErr := eval.ReadLiveConfig(e.stdio.Env)
	if readErr != nil && !errors.Is(readErr, eval.ErrLiveRequiresKey) {
		return 0, 0, readErr
	}
	liveCfg.Enabled = true
	liveCfg.Protocol = resolved.Protocol
	liveCfg.Model = resolved.Model
	liveCfg.BaseURL = resolved.BaseURL
	liveCfg.APIKey = resolved.APIKey
	if err := liveCfg.Validate(); err != nil {
		return 0, 0, err
	}

	set, err := eval.DiscoverHistoryTasks(ctx, spec.Repo, eval.HistoryOptions{
		MaxTasks: spec.MaxTasks,
		Since:    spec.Since,
	})
	if err != nil {
		return 0, 0, err
	}
	if len(set.Scenarios) == 0 {
		return 0, 0, errors.New("eval history: no commits with added test files found")
	}
	// 抽取出的场景没有 model（normalize 会填 "scripted"），统一覆写为真实模型名。
	for i := range set.Scenarios {
		set.Scenarios[i].Model = resolved.Model
	}

	opts := eval.Options{
		ChatterFactory:   eval.LiveChatterAdapter(resolved.Chatter),
		WorkspaceFactory: set.WorktreeFactory(spec.Repo),
		Out:              out,
		Live:             true,
		Verbose:          true,
	}
	// 与 live 路径相同的副本技巧：live=true 的副本负责采集 usage，
	// 不污染 e 自身，默认 RunSuite 路径逐字节不变。
	run := (&evaler{stdio: e.stdio, cwd: e.cwd, live: true}).runScenario

	runCtx, cancel := context.WithTimeout(ctx, liveSuiteTimeout(e.stdio.Env))
	defer cancel()
	report, err := eval.Run(runCtx, set.Scenarios, run, opts)
	return report.Passed, report.Failed, err
}
