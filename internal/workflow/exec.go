package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Step and run statuses.
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// defaultBackoff is the delay before the first retry; it doubles per attempt.
const defaultBackoff = 100 * time.Millisecond

// Prompter sends one prompt to the model and returns its answer.
type Prompter interface {
	Prompt(ctx context.Context, text string) (string, error)
}

// ToolRunner calls one tool by name with raw JSON arguments.
type ToolRunner interface {
	Run(ctx context.Context, name, args string) (string, error)
}

// Executor runs a definition against the injected model and tool seams.
type Executor struct {
	Prompter Prompter
	Tools    ToolRunner
	// Backoff is the base retry delay (default 100ms, doubled per attempt).
	Backoff time.Duration
}

// StepResult is the outcome of one step.
type StepResult struct {
	Name     string
	Status   string
	Output   string
	Error    string
	Attempts int
	Duration time.Duration
}

// Run is the outcome of one workflow.
type Run struct {
	Name   string
	Status string
	Steps  []StepResult
}

// Succeeded reports whether every step ended ok or was skipped.
func (r Run) Succeeded() bool { return r.Status == StatusOK }

// Output returns the output of a step by name.
func (r Run) Output(name string) (string, bool) {
	for _, step := range r.Steps {
		if step.Name == name {
			return step.Output, step.Status == StatusOK
		}
	}
	return "", false
}

// Run executes the steps in order and returns every result.
//
// A step that fails is retried up to step.Retries extra times with exponential
// backoff; once the attempts are exhausted the step's on_failure policy decides
// whether the run stops or continues. Errors are returned only for an invalid
// definition — failures are part of the result.
func (e *Executor) Run(ctx context.Context, def *Definition, input string) (Run, error) {
	if def == nil {
		return Run{}, errors.New("workflow: the definition is nil")
	}
	local := *def
	local.Steps = append([]Step(nil), def.Steps...)
	local.normalise()
	if err := local.Validate(); err != nil {
		return Run{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	backoff := e.Backoff
	if backoff <= 0 {
		backoff = defaultBackoff
	}
	for _, step := range local.Steps {
		if step.Tool != "" && e.Tools == nil {
			return Run{}, fmt.Errorf("workflow: step %q needs a tool runner for %q", step.Name, step.Tool)
		}
		if step.Tool == "" && e.Prompter == nil {
			return Run{}, fmt.Errorf("workflow: step %q needs a prompter", step.Name)
		}
	}

	run := Run{Name: local.Name, Status: StatusOK}
	outputs := make(map[string]string, len(local.Steps))
	var previous string
	havePrevious := false
	stopped := false

	for _, step := range local.Steps {
		if stopped {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  "an earlier step stopped the workflow",
			})
			continue
		}
		if !step.When.matches(previous, havePrevious) {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  "condition not met",
			})
			continue
		}

		result := e.runStep(ctx, step, input, outputs, backoff)
		run.Steps = append(run.Steps, result)
		if result.Status == StatusOK {
			outputs[step.Name] = result.Output
			previous = result.Output
			havePrevious = true
			continue
		}

		run.Status = StatusFailed
		if step.OnFailure != OnFailureContinue {
			stopped = true
		}
	}
	return run, nil
}

// runStep performs one step, retrying with backoff.
func (e *Executor) runStep(ctx context.Context, step Step, input string, outputs map[string]string, backoff time.Duration) StepResult {
	attempts := 1 + step.Retries
	if step.OnFailure == OnFailureRetry && attempts < 2 {
		attempts = 2
	}
	result := StepResult{Name: step.Name}
	started := time.Now()

	var output string
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		result.Attempts = attempt
		if attempt > 1 {
			if err := sleep(ctx, backoffAt(backoff, attempt-1)); err != nil {
				lastErr = err
				break
			}
		}
		output, lastErr = e.attempt(ctx, step, input, outputs)
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			// Cancelled or out of time: another attempt cannot help.
			break
		}
	}
	result.Duration = time.Since(started)
	if lastErr != nil {
		result.Status = StatusFailed
		result.Error = lastErr.Error()
		return result
	}
	result.Status = StatusOK
	result.Output = output
	return result
}

// attempt runs one step once, honouring the per-step timeout.
func (e *Executor) attempt(ctx context.Context, step Step, input string, outputs map[string]string) (string, error) {
	attemptCtx := ctx
	if timeout := step.Timeout.Std(); timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if step.Tool != "" {
		if e.Tools == nil {
			return "", fmt.Errorf("workflow: step %q needs a tool runner for %q", step.Name, step.Tool)
		}
		args := Substitute(step.Args, input, outputs)
		out, err := e.Tools.Run(attemptCtx, step.Tool, args)
		return out, timedOut(step, err, step.Timeout.Std())
	}
	if e.Prompter == nil {
		return "", fmt.Errorf("workflow: step %q needs a prompter", step.Name)
	}
	out, err := e.Prompter.Prompt(attemptCtx, Substitute(step.Prompt, input, outputs))
	return out, timedOut(step, err, step.Timeout.Std())
}

// timedOut re-labels a deadline error so the result says which step and how
// long it was allowed to run.
func timedOut(step Step, err error, limit time.Duration) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || limit <= 0 {
		return err
	}
	return fmt.Errorf("workflow: step %q timed out after %s", step.Name, limit)
}

// backoffAt doubles the base delay, capped so a large retry budget cannot
// overflow the duration.
func backoffAt(base time.Duration, attempt int) time.Duration {
	if attempt > 20 {
		attempt = 20
	}
	return base << uint(attempt-1)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Substitute replaces {{input}} and {{steps.NAME.output}} placeholders. Keys
// with no value are left untouched so a template meant for the model survives.
func Substitute(text, input string, outputs map[string]string) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	var out strings.Builder
	rest := text
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			out.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}}")
		if end < 0 {
			out.WriteString(rest)
			break
		}
		end += start
		out.WriteString(rest[:start])
		key := strings.TrimSpace(rest[start+2 : end])
		switch {
		case key == "input":
			out.WriteString(input)
		case strings.HasPrefix(key, "steps.") && strings.HasSuffix(key, ".output"):
			name := strings.TrimSuffix(strings.TrimPrefix(key, "steps."), ".output")
			if value, ok := outputs[name]; ok {
				out.WriteString(value)
			} else {
				out.WriteString(rest[start : end+2])
			}
		default:
			out.WriteString(rest[start : end+2])
		}
		rest = rest[end+2:]
	}
	return out.String()
}
