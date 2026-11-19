package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/agent"
	"github.com/2118505078xyh1/argent-go/internal/cli"
	"github.com/2118505078xyh1/argent-go/internal/session"
)

// NewEvaler returns the cli.EvalRunner used by `argent eval`.
//
// It runs each scenario in the current workspace with the same wiring as a
// normal prompt, and reports the tool names the model called so the command can
// grade expectations.
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
	return cli.EvalReport{Text: result.Text, Tools: toolNames(result)}, runErr
}

func toolNames(result agent.Result) []string {
	names := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	for _, message := range result.Messages {
		for _, call := range message.ToolCalls {
			name := strings.TrimSpace(call.Function.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
