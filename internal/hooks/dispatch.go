package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// maxCapture bounds one stream captured from a hook.
	maxCapture = 64 * 1024
	// summaryClip bounds the output quoted in a Result.Message.
	summaryClip = 400
)

// Payload is the JSON document written to a hook's stdin.
type Payload struct {
	Event     string          `json:"event"`
	Tool      string          `json:"tool,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	CWD       string          `json:"cwd,omitempty"`
	// Prompt is the user message for UserPromptSubmit and Stop.
	Prompt string `json:"prompt,omitempty"`
	// Output is the tool result for PostToolUse.
	Output string `json:"output,omitempty"`
	// Error is the tool failure for PostToolUseFailure.
	Error string `json:"error,omitempty"`
}

// Result is the aggregate decision of one dispatch.
//
// Blocked is true only when a PreToolUse hook exited with status 2. Message
// carries the blocking hook's output, or the warnings of the hooks that failed
// without blocking.
type Result struct {
	Blocked bool
	Message string
}

// Outcome is what one hook command did.
type Outcome struct {
	Event    Event
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	Blocked  bool
	Elapsed  time.Duration
	Err      error
}

// Message is a one-line summary of the outcome.
func (o Outcome) Message() string {
	detail := strings.TrimSpace(o.Stdout)
	if detail == "" {
		detail = strings.TrimSpace(o.Stderr)
	}
	detail = clip(detail)

	var prefix string
	switch {
	case o.TimedOut:
		prefix = fmt.Sprintf("%s hook %q timed out after %s", o.Event, clipCommand(o.Command), o.Elapsed.Round(time.Millisecond))
	case o.Err != nil:
		prefix = fmt.Sprintf("%s hook %q could not run: %v", o.Event, clipCommand(o.Command), o.Err)
	case o.Blocked:
		prefix = fmt.Sprintf("%s hook %q blocked the call (exit 2)", o.Event, clipCommand(o.Command))
	case o.ExitCode != 0:
		prefix = fmt.Sprintf("%s hook %q failed (exit %d)", o.Event, clipCommand(o.Command), o.ExitCode)
	default:
		prefix = fmt.Sprintf("%s hook %q succeeded", o.Event, clipCommand(o.Command))
	}
	if detail == "" {
		return prefix
	}
	return prefix + ": " + detail
}

// Dispatcher runs hook commands.
type Dispatcher struct {
	// Timeout bounds one command when the command itself sets none; zero
	// means 30 seconds.
	Timeout time.Duration
	// Shell overrides the command interpreter, for tests and odd platforms.
	// The default is bash -c (cmd /C on Windows when bash is absent).
	Shell func(command string) (name string, args []string)
}

// Dispatch runs every matching hook for payload.Event and returns the
// aggregate decision.
func (d *Dispatcher) Dispatch(ctx context.Context, config *Config, payload Payload) (Result, error) {
	outcomes, err := d.Run(ctx, config, payload)
	if err != nil {
		return Result{}, err
	}
	return Summarize(outcomes), nil
}

// DispatchDocument runs the hooks of a trusted document.
func (d *Dispatcher) DispatchDocument(ctx context.Context, document *Document, payload Payload) (Result, error) {
	if document == nil || document.Config == nil {
		return Result{}, ErrUntrusted
	}
	return d.Dispatch(ctx, document.Config, payload)
}

// Run executes the synchronous hooks for one event, in declaration order.
// Async hooks are skipped: a deciding hook must answer synchronously.
func (d *Dispatcher) Run(ctx context.Context, config *Config, payload Payload) ([]Outcome, error) {
	if config == nil {
		return nil, nil
	}
	event, ok := ParseEvent(payload.Event)
	if !ok {
		return nil, fmt.Errorf("hooks: unknown event %q", payload.Event)
	}

	commands := config.ForEvent(event, payload.Tool)
	outcomes := make([]Outcome, 0, len(commands))
	for _, command := range commands {
		if command.Async {
			continue
		}
		outcomes = append(outcomes, d.RunCommand(ctx, command, payload))
		if ctx.Err() != nil {
			return outcomes, ctx.Err()
		}
	}
	return outcomes, nil
}

// RunCommand runs one hook command with the payload on stdin.
func (d *Dispatcher) RunCommand(ctx context.Context, command Command, payload Payload) Outcome {
	started := time.Now()
	outcome := Outcome{Event: command.Event, Command: command.Command, ExitCode: -1}

	if len(bytes.TrimSpace(payload.Args)) == 0 {
		payload.Args = json.RawMessage(`{}`)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		outcome.Err = err
		outcome.Elapsed = time.Since(started)
		return outcome
	}

	timeout := command.Timeout
	if timeout <= 0 {
		timeout = d.Timeout
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if timeout < minTimeout {
		timeout = minTimeout
	}

	name, args := shellCommand(command.Command)
	if d.Shell != nil {
		name, args = d.Shell(command.Command)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(runCtx, name, args...)
	if payload.CWD != "" {
		process.Dir = payload.CWD
	}
	process.Env = append(os.Environ(), "ARGENT_HOOK_EVENT="+string(command.Event))
	if payload.CWD != "" {
		process.Env = append(process.Env,
			"ARGENT_PROJECT_DIR="+payload.CWD,
			"CLAUDE_PROJECT_DIR="+payload.CWD,
		)
	}
	process.Stdin = bytes.NewReader(body)
	stdout := &limitedBuffer{}
	stderr := &limitedBuffer{}
	process.Stdout = stdout
	process.Stderr = stderr

	runErr := process.Run()
	outcome.Elapsed = time.Since(started)
	outcome.Stdout = stdout.String()
	outcome.Stderr = stderr.String()

	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		outcome.TimedOut = true
		outcome.ExitCode = -1
	case runErr == nil:
		outcome.ExitCode = 0
	default:
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			outcome.ExitCode = exitErr.ExitCode()
		} else {
			outcome.Err = runErr
		}
	}

	// Exit status 2 is the hook protocol's "block" signal, and only PreToolUse
	// may block an action.
	if command.Event == PreToolUse && outcome.ExitCode == 2 && !outcome.TimedOut && command.BlocksOnFailure() {
		outcome.Blocked = true
	}
	return outcome
}

// Summarize turns outcomes into the block/warn decision.
func Summarize(outcomes []Outcome) Result {
	var result Result
	var messages []string
	for _, outcome := range outcomes {
		if outcome.Blocked {
			result.Blocked = true
			messages = append(messages, outcome.Message())
			continue
		}
		if outcome.ExitCode != 0 || outcome.TimedOut || outcome.Err != nil {
			messages = append(messages, outcome.Message())
		}
	}
	result.Message = strings.Join(messages, "\n")
	return result
}

// shellCommand picks the interpreter for a hook command. Hooks are shell
// scripts, so bash is preferred when a working one is installed.
func shellCommand(command string) (string, []string) {
	if bash := findBash(); bash != "" {
		return bash, []string{"-c", command}
	}
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "/bin/sh", []string{"-c", command}
}

var (
	bashOnce sync.Once
	bashPath string
)

// findBash returns a bash that actually runs. On Windows the first "bash" on
// PATH is often the WSL alias in System32, which fails when no distro is
// installed, so each candidate is probed once.
func findBash() string {
	bashOnce.Do(func() {
		var candidates []string
		if found, err := exec.LookPath("bash"); err == nil {
			candidates = append(candidates, found)
		}
		if runtime.GOOS == "windows" {
			candidates = append(candidates,
				`C:\Program Files\Git\bin\bash.exe`,
				`C:\Program Files\Git\usr\bin\bash.exe`,
				`C:\Program Files (x86)\Git\bin\bash.exe`,
				`C:\msys64\usr\bin\bash.exe`,
			)
		}
		for _, candidate := range candidates {
			if bashRuns(candidate) {
				bashPath = candidate
				return
			}
		}
	})
	return bashPath
}

func bashRuns(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "-c", "exit 0").Run() == nil
}

// limitedBuffer captures at most maxCapture bytes.
type limitedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxCapture - b.buf.Len(); room > 0 {
		if len(p) > room {
			_, _ = b.buf.Write(p[:room])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else {
		b.truncated = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	if b.truncated {
		return b.buf.String() + "\n…(truncated)"
	}
	return b.buf.String()
}

func clip(text string) string {
	if len(text) <= summaryClip {
		return text
	}
	return text[:summaryClip] + "…"
}

func clipCommand(command string) string {
	const limit = 120
	if len(command) <= limit {
		return command
	}
	return command[:limit] + "…"
}
