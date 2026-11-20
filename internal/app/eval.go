package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/eval"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// NewEvaler 返回供 `argent eval` 使用的 cli.EvalRunner。
//
// 套件运行器使用 internal/eval 的脚本化提供方，配合生产环境的 agent 循环、工具注册表和安全链。
// RunScenario 保留为只拥有提示的调用方所使用的窄接缝。
func NewEvaler(stdio cli.IO) cli.EvalRunner {
	return &evaler{stdio: stdio}
}

type evaler struct {
	stdio cli.IO
	cwd   string
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
	report, err := eval.Run(ctx, scenarios, e.runScenario, eval.Options{Out: out})
	return report.Passed, report.Failed, err
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

	loop := &agent.Runner{Opts: agent.Options{
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
	}}
	result, err := loop.Run(runCtx, workspace.Prompt)

	trace := eval.Trace{
		Text:    result.Text,
		Tools:   append([]string(nil), toolNamesAll(result)...),
		Stopped: result.Stopped != "" && result.Stopped != agent.StopComplete,
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
