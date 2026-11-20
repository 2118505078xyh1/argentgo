package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/proc"
)

// Step and run statuses.
const (
	StatusOK      = "ok"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// defaultBackoff is the delay before the first retry; it doubles per attempt.
const defaultBackoff = 100 * time.Millisecond

// defaultCheckTimeout bounds an objective success check or a cleanup command
// when the definition does not give one.
const defaultCheckTimeout = 30 * time.Second

// maxCheckDiagnostic caps the command output copied into a failure message.
const maxCheckDiagnostic = 8 << 10

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
	if lastErr != nil {
		result.Status = StatusFailed
		result.Error = lastErr.Error()
		// Cleanup runs once, after all attempts are exhausted and before the
		// caller applies the step's on_failure policy. Its output is added to
		// the diagnostic but its exit code cannot change the decision.
		if cleanup := runCleanupCommand(ctx, step); cleanup != "" {
			result.Error += "\n" + cleanup
		}
		result.Duration = time.Since(started)
		return result
	}
	result.Duration = time.Since(started)
	result.Status = StatusOK
	result.Output = output
	return result
}

// attempt runs one step once, honouring the per-step timeout. A configured
// success check is part of the attempt, so a failed check is retried by runStep
// exactly like a tool or prompter error.
func (e *Executor) attempt(ctx context.Context, step Step, input string, outputs map[string]string) (string, error) {
	attemptCtx := ctx
	if timeout := step.Timeout.Std(); timeout > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var (
		out string
		err error
	)
	if step.Tool != "" {
		if e.Tools == nil {
			return "", fmt.Errorf("workflow: step %q needs a tool runner for %q", step.Name, step.Tool)
		}
		args := Substitute(step.Args, input, outputs)
		out, err = e.Tools.Run(attemptCtx, step.Tool, args)
		err = timedOut(step, err, step.Timeout.Std())
	} else {
		if e.Prompter == nil {
			return "", fmt.Errorf("workflow: step %q needs a prompter", step.Name)
		}
		out, err = e.Prompter.Prompt(attemptCtx, Substitute(step.Prompt, input, outputs))
		err = timedOut(step, err, step.Timeout.Std())
	}
	if err != nil {
		return out, err
	}
	if step.SuccessCheck != nil && strings.TrimSpace(step.SuccessCheck.Command) != "" {
		if err := runSuccessCheck(ctx, step); err != nil {
			return out, err
		}
	}
	return out, nil
}

// timedOut re-labels a deadline error so the result says which step and how
// long it was allowed to run.
func timedOut(step Step, err error, limit time.Duration) error {
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || limit <= 0 {
		return err
	}
	return fmt.Errorf("workflow: step %q timed out after %s", step.Name, limit)
}

// checkTimeout resolves a TimeoutSeconds value; zero or negative selects the
// conservative default.
func checkTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultCheckTimeout
	}
	return time.Duration(seconds) * time.Second
}

// diagnosticTail keeps at most n bytes from the end of s. The cut is moved to a
// rune boundary so a multibyte character is never split in half.
func diagnosticTail(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// commandDiagnostic picks the bounded output of a command for a failure
// message, preferring stderr because that is where checks report errors.
func commandDiagnostic(res proc.Result) string {
	if text := strings.TrimSpace(res.Stderr); text != "" {
		return diagnosticTail(text, maxCheckDiagnostic)
	}
	return diagnosticTail(strings.TrimSpace(res.Stdout), maxCheckDiagnostic)
}

func diagnosticSuffix(text string) string {
	if text == "" {
		return ""
	}
	return ": " + text
}

// runSuccessCheck verifies a step objectively through proc.Shell/proc.Run, the
// same shell path every other command in the codebase uses. A non-zero exit, a
// spawn error or a timeout becomes an error so runStep retries it through the
// normal failure path.
func runSuccessCheck(ctx context.Context, step Step) error {
	check := step.SuccessCheck
	command := strings.TrimSpace(check.Command)
	limit := checkTimeout(check.TimeoutSeconds)
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	cmd := proc.Shell(command)
	cmd.Timeout = limit
	// A check can spawn its own children (sleep, build tools); kill the whole
	// tree on timeout so the step cannot outlive its budget.
	cmd.KillTree = true
	res, err := proc.Run(runCtx, cmd)
	diagnostic := diagnosticSuffix(commandDiagnostic(res))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("workflow: step %q success check timed out after %s%s", step.Name, limit, diagnostic)
		}
		return fmt.Errorf("workflow: step %q success check %q failed: %w%s", step.Name, command, err, diagnostic)
	}
	if !res.Success() {
		return fmt.Errorf("workflow: step %q success check %q exited %d%s", step.Name, command, res.ExitCode, diagnostic)
	}
	return nil
}

// runCleanupCommand runs the step's cleanup command once and returns text that
// is appended to the failure diagnostic. Its exit code is reported but never
// changes the failure decision.
func runCleanupCommand(ctx context.Context, step Step) string {
	command := strings.TrimSpace(step.CleanupCommand)
	if command == "" {
		return ""
	}
	limit := checkTimeout(0)
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	cmd := proc.Shell(command)
	cmd.Timeout = limit
	// Cleanup can spawn its own children; kill the whole tree on timeout so it
	// cannot hold the run open after the step has already failed.
	cmd.KillTree = true
	res, err := proc.Run(runCtx, cmd)
	var b strings.Builder
	fmt.Fprintf(&b, "cleanup %q: ", command)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintf(&b, "timed out after %s", limit)
	case err != nil:
		fmt.Fprintf(&b, "%v", err)
	default:
		fmt.Fprintf(&b, "exit %d", res.ExitCode)
	}
	b.WriteString(diagnosticSuffix(commandDiagnostic(res)))
	return b.String()
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
