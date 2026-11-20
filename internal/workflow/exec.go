package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
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

// maxCheckAnchorOutput bounds the redacted check output copied into the retry
// hint. It is deliberately smaller than a raw diagnostic: the hint is a nudge,
// not a second copy of the command output.
const maxCheckAnchorOutput = 512

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

// Run executes the steps in dependency order and returns every result.
// Definitions with declared parameters need their values supplied through
// RunWithParams.
func (e *Executor) Run(ctx context.Context, def *Definition, input string) (Run, error) {
	return e.RunWithParams(ctx, def, input, nil)
}

// RunWithParams executes the steps in dependency order (a step reading
// {{steps.NAME.output}} runs after its producer), additionally resolving {{key}}
// placeholders from the definition's declared parameters. Values are resolved
// here as well as by the CLI so a caller cannot bypass required/default checks
// by passing raw values; re-resolving an already resolved map is idempotent.
//
// A step that fails is retried up to step.Retries extra times with exponential
// backoff; once the attempts are exhausted the step's on_failure policy decides
// whether the run stops or continues. Errors are returned only for an invalid
// definition or unresolved parameters — step failures are part of the result.
func (e *Executor) RunWithParams(ctx context.Context, def *Definition, input string, params map[string]string) (Run, error) {
	if def == nil {
		return Run{}, errors.New("workflow: the definition is nil")
	}
	local := *def
	local.Steps = append([]Step(nil), def.Steps...)
	local.normalise()
	if err := local.Validate(); err != nil {
		return Run{}, err
	}
	resolved, err := ResolveParameters(local, params)
	if err != nil {
		return Run{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Validate() has already rejected cycles and dangling step references; the
	// order it computed there is the order execution must follow. A step that
	// reads {{steps.NAME.output}} has to run after its producer, otherwise the
	// token is left unsubstituted in the prompt or tool args.
	order, err := ValidateDAG(local.Steps)
	if err != nil {
		return Run{}, err
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

	for _, index := range order {
		step := local.Steps[index]
		if stopped {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  "an earlier step stopped the workflow",
			})
			continue
		}
		// A {{steps.NAME.output}} reference is a real dependency, not just a
		// template hint: the consumer must not run when its producer produced
		// no output. The global stop flag is not enough here because a producer
		// can fail under on_failure:continue, or be skipped by its own When
		// condition, leaving the consumer with a literal unresolved token.
		missing := ""
		for _, dep := range referencedSteps(step) {
			if _, ok := outputs[dep]; !ok {
				missing = dep
				break
			}
		}
		if missing != "" {
			run.Steps = append(run.Steps, StepResult{
				Name:   step.Name,
				Status: StatusSkipped,
				Error:  fmt.Sprintf("depends on step %q which did not produce an output", missing),
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

		result := e.runStep(ctx, step, input, outputs, resolved, backoff)
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
func (e *Executor) runStep(ctx context.Context, step Step, input string, outputs, params map[string]string, backoff time.Duration) StepResult {
	attempts := 1 + step.Retries
	if step.OnFailure == OnFailureRetry && attempts < 2 {
		attempts = 2
	}
	result := StepResult{Name: step.Name}
	started := time.Now()

	var output string
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		// A finished context cannot be recovered by another attempt. Check
		// before invoking the seam: a prompter or tool runner may ignore its
		// context, and a cancelled run must still stop without starting new
		// work. Attempts stays zero when nothing was actually attempted.
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		result.Attempts = attempt
		if attempt > 1 {
			if err := sleep(ctx, backoffAt(backoff, attempt-1)); err != nil {
				lastErr = err
				break
			}
		}
		output, lastErr = e.attempt(ctx, step, input, outputs, params)
		if err := ctx.Err(); err != nil {
			// Cancelled or out of time: another attempt cannot help. If the
			// seam ignored cancellation and returned a value, surface the
			// context error so the cancelled step is not reported as a success.
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		// A failed success check is re-run by this loop, so tell the model not
		// to repeat it unchanged. The hint is added only when a retry was
		// configured; the check's own diagnostics stay byte-identical.
		var failedCheck *checkFailure
		if attempts > 1 && errors.As(lastErr, &failedCheck) {
			lastErr = fmt.Errorf("%w\n%s", lastErr, checkRetryHint(step, failedCheck.output))
		}
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
func (e *Executor) attempt(ctx context.Context, step Step, input string, outputs, params map[string]string) (string, error) {
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
		args := Substitute(step.Args, input, outputs, params)
		out, err = e.Tools.Run(attemptCtx, step.Tool, args)
		err = timedOut(step, err, step.Timeout.Std())
	} else {
		if e.Prompter == nil {
			return "", fmt.Errorf("workflow: step %q needs a prompter", step.Name)
		}
		out, err = e.Prompter.Prompt(attemptCtx, Substitute(step.Prompt, input, outputs, params))
		err = timedOut(step, err, step.Timeout.Std())
	}
	if err != nil {
		return out, err
	}
	if step.SuccessCheck != nil && strings.TrimSpace(step.SuccessCheck.Command) != "" {
		if err := runSuccessCheck(attemptCtx, step); err != nil {
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
	return fmt.Errorf("workflow: step %q timed out after %s: %w", step.Name, limit, err)
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

// checkFailure wraps a failed success check so runStep can recognise it and add
// a bounded retry hint. The original message and error chain are preserved: the
// Error text is unchanged and Unwrap keeps DeadlineExceeded matchable.
type checkFailure struct {
	output string
	cause  error
}

func (c *checkFailure) Error() string { return c.cause.Error() }
func (c *checkFailure) Unwrap() error { return c.cause }

// checkRetryHint mirrors agent.VerificationFeedback for the workflow layer,
// which is not allowed to import agent. It redacts the check output before
// clipping it to a rune-safe tail, then adds a next-step instruction so a retry
// does not re-run the very same check against unchanged code.
func checkRetryHint(step Step, output string) string {
	label := strings.ToUpper(strings.TrimSpace(step.Name))
	if label == "" {
		label = "WORKFLOW"
	}
	var b strings.Builder
	b.WriteString("[verify: " + label + "] FAIL")
	if clean := llm.RedactText(strings.TrimSpace(output)); clean != "" {
		b.WriteString("\n" + diagnosticTail(clean, maxCheckAnchorOutput))
	}
	b.WriteString("\ndo not re-run the same check unchanged; adjust the code first.")
	return b.String()
}

// runSuccessCheck verifies a step objectively through proc.Shell/proc.Run, the
// same shell path every other command in the codebase uses. A non-zero exit, a
// spawn error or a timeout becomes an error so runStep retries it through the
// normal failure path.
func runSuccessCheck(ctx context.Context, step Step) error {
	check := step.SuccessCheck
	command := strings.TrimSpace(check.Command)
	limit := checkTimeout(check.TimeoutSeconds)
	// The check is part of the attempt, so it must never outlive the attempt's
	// remaining budget. When the context carries a deadline (the step timeout),
	// clamp the configured check timeout to the time still left on that budget.
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < limit {
			limit = remaining
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	cmd := proc.Shell(command)
	cmd.Timeout = limit
	// A check can spawn its own children (sleep, build tools); kill the whole
	// tree on timeout so the step cannot outlive its budget.
	cmd.KillTree = true
	res, err := proc.Run(runCtx, cmd)
	diagnostic := diagnosticSuffix(commandDiagnostic(res))
	var failure error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		failure = fmt.Errorf("workflow: step %q success check timed out after %s: %w%s", step.Name, limit, err, diagnostic)
	case err != nil:
		failure = fmt.Errorf("workflow: step %q success check %q failed: %w%s", step.Name, command, err, diagnostic)
	case !res.Success():
		failure = fmt.Errorf("workflow: step %q success check %q exited %d%s", step.Name, command, res.ExitCode, diagnostic)
	default:
		return nil
	}
	return &checkFailure{output: commandDiagnostic(res), cause: failure}
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

// Substitute replaces {{input}}, {{steps.NAME.output}} and, when params is
// supplied, {{key}} placeholders from resolved workflow parameters. Built-in
// tokens take precedence, and keys with no value are left untouched so a
// template meant for the model survives. Inserted values are written verbatim
// and never rescanned, so a value containing "{{...}}" cannot inject tokens.
func Substitute(text, input string, outputs map[string]string, params ...map[string]string) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	var out strings.Builder
	rest := text
	for {
		start, tokenEnd, key, ok := nextPlaceholder(rest)
		if !ok {
			out.WriteString(rest)
			break
		}
		end := tokenEnd - 2 // index of the first '}' of the closing "}}"
		out.WriteString(rest[:start])
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
			if value, ok := lookupParam(params, key); ok {
				out.WriteString(value)
			} else {
				out.WriteString(rest[start : end+2])
			}
		}
		rest = rest[end+2:]
	}
	return out.String()
}

// lookupParam finds the first map that holds key. The variadic argument of
// Substitute normally carries exactly one map; accepting more keeps the call
// site simple when a caller composes parameter sources.
func lookupParam(params []map[string]string, key string) (string, bool) {
	for _, p := range params {
		if value, ok := p[key]; ok {
			return value, true
		}
	}
	return "", false
}
