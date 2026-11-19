// Package agent runs the model/tool loop.
package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/2118505078xyh1/argent-go/internal/llm"
	"github.com/2118505078xyh1/argent-go/internal/tools"
)

const systemTemplate = `You are argent, a concise coding assistant working on the user's machine.
Working directory: %s
Use the available tools to inspect and change files. Read before writing, batch independent calls, and stop once the request is satisfied.`

// Options configures one run.
type Options struct {
	Model    string
	MaxTurns int
	Root     string
	Out      io.Writer
	Record   func(kind string, payload any)
}

// Runner drives the loop.
type Runner struct {
	Client *llm.Client
	Tools  *tools.Registry
	Opts   Options
}

// Run executes the prompt and returns the final assistant text.
func (r *Runner) Run(ctx context.Context, prompt string) (string, error) {
	maxTurns := r.Opts.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 12
	}
	messages := []llm.Message{
		{Role: "system", Content: fmt.Sprintf(systemTemplate, r.Opts.Root)},
		{Role: "user", Content: prompt},
	}
	r.record("user", prompt)

	for turn := 1; turn <= maxTurns; turn++ {
		var streamed strings.Builder
		reply, err := r.Client.Chat(ctx, r.Opts.Model, messages, r.Tools.Specs(), func(delta string) {
			streamed.WriteString(delta)
			if r.Opts.Out != nil {
				fmt.Fprint(r.Opts.Out, delta)
			}
		})
		if err != nil {
			return streamed.String(), err
		}
		messages = append(messages, llm.Message{
			Role:      reply.Role,
			Content:   reply.Content,
			ToolCalls: reply.ToolCalls,
		})
		r.record("assistant", reply)

		if len(reply.ToolCalls) == 0 {
			return reply.Content, nil
		}

		for _, call := range reply.ToolCalls {
			output, runErr := r.Tools.Run(ctx, call.Function.Name, call.Function.Arguments)
			if runErr != nil {
				output = "error: " + runErr.Error()
			}
			messages = append(messages, llm.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    output,
			})
			r.record("tool", map[string]string{
				"id":     call.ID,
				"name":   call.Function.Name,
				"output": output,
			})
		}
	}
	return "", fmt.Errorf("turn limit reached after %d turns", maxTurns)
}

func (r *Runner) record(kind string, payload any) {
	if r.Opts.Record != nil {
		r.Opts.Record(kind, payload)
	}
}
