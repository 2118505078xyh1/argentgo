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

// NewEvaler returns the cli.EvalRunner used by `argent eval`.
//
// The suite runner uses internal/eval's scripted provider with the production
// agent loop, tool registry and security chain. RunScenario remains as the
// narrow seam for callers that only have a prompt.
func NewEvaler(stdio cli.IO) cli.EvalRunner {
	return &evaler{stdio: stdio}
}

type evaler struct {
	stdio cli.IO
	cwd   string
}

// WithCwd points the evaluator at another workspace (tests use it).
func (e *evaler) WithCwd(cwd string) *evaler {
	e.cwd = cwd
	return e
}

// RunSuite executes a scenario document through the real agent loop. Each
// scenario gets its own scratch workspace from internal/eval; the provider is
// the script embedded in the document, so no network or API key is involved.
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
		// Eval scenarios assert exact request counts (e.g. the turn-limit case).
		// The one-shot final answer is covered by internal/agent tests instead.
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

// cancelAfterChatter stops the run before the model turn that would exceed the
// scenario's budget. The scripted provider is not called for that turn, so the
// harness counts exactly the turns that reached the model.
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
