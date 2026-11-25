package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/eval"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/session"
	"example.com/argentgo/internal/tools"
)

// NewEvaler 返回供 `argentgo eval` 使用的 cli.EvalRunner。
//
// 套件运行器使用 internal/eval 的脚本化提供方，配合生产环境的 agent 循环、工具注册表和安全链。
// RunScenario 保留为只拥有提示的调用方所使用的窄接缝。
func NewEvaler(stdio cli.IO) cli.EvalRunner {
	liveCfg, _ := eval.ReadLiveConfig(stdio.Env) // 错误在 RunSuite 中按门禁重新处理
	return &evaler{stdio: stdio, live: liveCfg.Enabled}
}

type evaler struct {
	stdio cli.IO
	cwd   string
	live  bool // ARGENT_EVAL_LIVE=1 时走真实 provider；默认 false 走脚本化，断网逐字节不变
}

// WithCwd 把评估器指向另一个工作区（测试使用）。
func (e *evaler) WithCwd(cwd string) *evaler {
	e.cwd = cwd
	return e
}

// RunSuite 通过真实的 agent 循环执行一份场景文档。每个场景都从 internal/eval 获得自己的临时工作区；
// 提供方是文档中内嵌的脚本，因此不涉及网络或 API key。
func (e *evaler) RunSuite(ctx context.Context, path string, out io.Writer) (int, int, error) {
	scenarios, err := eval.Load(path)
	if err != nil {
		return 0, 0, err
	}
	opts := eval.Options{Out: out}
	if e.live {
		// 真实评测（opt-in）：无 key 时明确报错，绝不静默降级到脚本化（否则
		// 看起来像真实评测其实不是）。错误文本固定，不拼接 key。
		cfg, err := eval.ReadLiveConfig(e.stdio.Env)
		if err != nil {
			return 0, 0, err
		}
		if err := cfg.Validate(); err != nil {
			return 0, 0, err
		}
		resolved, err := Resolve(e.stdio, cli.Options{Cwd: e.cwd})
		if err != nil {
			return 0, 0, errors.New("eval live mode: resolve failed")
		}
		if strings.TrimSpace(resolved.APIKey) == "" {
			return 0, 0, errors.New("ARGENT_EVAL_LIVE=1 requires an API key (flag/config/login/provider env)")
		}
		opts.ChatterFactory = eval.LiveChatterAdapter(resolved.Chatter)
	}
	report, err := eval.Run(ctx, scenarios, e.runScenario, opts)
	return report.Passed, report.Failed, err
}

// RunOrchestration 实现 cli.OrchestrationRunner：读取编排场景文档，用真实 agent
// 循环运行，并把 internal/eval 的报告映射为 cli 的窄报告类型。它是 `eval
// orchestration` 带外前缀的注入目标，默认 eval 路径不依赖它。
func (e *evaler) RunOrchestration(ctx context.Context, path string) (cli.OrchestrationReport, error) {
	scenarios, err := eval.LoadOrchestration(path)
	if err != nil {
		return cli.OrchestrationReport{}, err
	}
	source := eval.RunOrchestration(ctx, scenarios)
	report := cli.OrchestrationReport{
		Total:  source.Total,
		Passed: source.Passed,
		Failed: source.Failed,
	}
	for _, failure := range source.Failures {
		report.Failures = append(report.Failures, cli.OrchestrationFailure{
			ID:     failure.ID,
			Name:   failure.Name,
			Reason: failure.Reason,
		})
	}
	return report, nil
}

func (e *evaler) runScenario(ctx context.Context, scenario eval.Scenario, workspace eval.Workspace) (eval.Trace, error) {
	storage, err := os.MkdirTemp("", "argent-eval-store-")
	if err != nil {
		return eval.Trace{}, err
	}
	defer os.RemoveAll(storage)

	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		return eval.Trace{}, err
	}
	sess, err := store.NewSession("eval: " + firstLine(workspace.Prompt, 40))
	if err != nil {
		return eval.Trace{}, err
	}

	root := workspace.Dir
	if strings.TrimSpace(e.cwd) != "" {
		root = e.cwd
	}
	policy := security.NewPolicy(root, security.PolicyConfig{
		DenyPaths:  append([]string(nil), scenario.DenyPaths...),
		WriteRoots: []string{root},
	})
	approver := security.Approver(autoApprover{})
	if scenario.Approve != nil && !*scenario.Approve {
		approver = security.NeverApprover{}
	}
	audit := security.NewAuditLog(filepath.Join(storage, "audit"))
	chain := security.NewChain(policy, approver, audit, sess.ID())

	registry := tools.NewWithLimit(root, true, 0)
	registry.SetSessionDir(filepath.Join(storage, "sessions", sess.ID()))
	registry.SetGuard(chain)

	maxTurns := scenario.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 12
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	chatter := llm.Chatter(workspace.Chatter)
	if scenario.CancelAfter > 0 {
		chatter = &cancelAfterChatter{inner: chatter, after: scenario.CancelAfter, cancel: cancel}
	}

	// 真实评测（opt-in）时从每轮 EventUsage 采集 token 细分。仅在 live 时安装 Emit，
	// 默认（脚本化）时 opts 不含 Emit，usage 采集路径完全不触发，行为逐字节不变。
	var traceUsage llm.Usage
	var loopOpts = agent.Options{
		Model:    workspace.Model,
		MaxTurns: maxTurns,
		Root:     root,
		System:   workspace.System,
		Chatter:  chatter,
		Tools:    registry,
		Budget:   agent.Budget{MaxTurns: maxTurns},
		// eval 场景会断言精确的请求次数（例如轮次限制的情形）。
		// 一次性最终答案改由 internal/agent 的测试覆盖。
		NoFinalAnswer: true,
	}
	if e.live {
		loopOpts.Emit = func(ev agent.Event) {
			if ev.Kind == agent.EventUsage {
				u := ev.Usage
				traceUsage.PromptTokens += u.PromptTokens
				traceUsage.CompletionTokens += u.CompletionTokens
				traceUsage.CachedTokens += u.CachedTokens
				traceUsage.CacheWriteTokens += u.CacheWriteTokens
			}
		}
	}
	loop := &agent.Runner{Opts: loopOpts}
	result, err := loop.Run(runCtx, workspace.Prompt)

	trace := eval.Trace{
		Text:    result.Text,
		Tools:   append([]string(nil), toolNamesAll(result)...),
		Stopped: result.Stopped != "" && result.Stopped != agent.StopComplete,
		Usage:   traceUsage,
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			trace.Stopped = true
			return trace, err
		}
		return trace, err
	}
	return trace, nil
}

// cancelAfterChatter 在会超出场景预算的模型轮次之前停止运行。该轮次不会调用脚本化提供方，
// 因此测试框架统计的正是真正到达模型的轮次。
type cancelAfterChatter struct {
	inner  llm.Chatter
	after  int
	calls  int
	cancel context.CancelFunc
}

func (c *cancelAfterChatter) Chat(ctx context.Context, model string, messages []llm.Message, specs []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	if c.calls >= c.after {
		c.cancel()
		return llm.Message{}, ctx.Err()
	}
	c.calls++
	return c.inner.Chat(ctx, model, messages, specs, onText)
}

func toolNamesAll(result agent.Result) []string {
	var names []string
	for _, message := range result.Messages {
		for _, call := range message.ToolCalls {
			if name := strings.TrimSpace(call.Function.Name); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func (e *evaler) RunScenario(ctx context.Context, prompt string) (cli.EvalReport, error) {
	resolved, err := Resolve(e.stdio, cli.Options{Cwd: e.cwd})
	if err != nil {
		return cli.EvalReport{}, err
	}
	store, err := session.Open(filepath.Join(resolved.Storage, "sessions"))
	if err != nil {
		return cli.EvalReport{}, err
	}
	sess, err := store.NewSession("eval: " + firstLine(prompt, 40))
	if err != nil {
		return cli.EvalReport{}, err
	}

	result, runErr := Execute(ctx, e.stdio, resolved, sess, prompt, nil)
	return cli.EvalReport{Text: result.Text, Tools: toolNamesAll(result)}, runErr
}

func toolNames(result agent.Result) []string {
	names := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	for _, name := range toolNamesAll(result) {
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}
