package eval

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultExecTimeout = 30 * time.Second
	maxExecTimeout     = 120 * time.Second
	execOutputCap      = 64 * 1024
	execTruncMarker    = "...[output truncated]..."
	// execShellMeta lists command separators, redirections, substitutions and
	// control characters. No shell is used here, so these are rejected as
	// defence in depth: a future refactor must not accidentally make them live.
	execShellMeta = ";|&<>()$`'\";\n\r\x00^"
)

// Sentinel errors so callers can classify a rejected fixture with errors.Is.
var (
	ErrExecEmptyCommand    = errors.New("eval: exec check: empty command")
	ErrExecCommandDenied   = errors.New("eval: exec check: command is not allowlisted")
	ErrExecGoSubcommand    = errors.New("eval: exec check: go subcommand must be test or build")
	ErrExecBadPath         = errors.New("eval: exec check: executable path must be relative and stay inside the workspace")
	ErrExecShellMeta       = errors.New("eval: exec check: shell metacharacters are not allowed")
	ErrExecDangerousGoFlag = errors.New("eval: exec check: go flag can run arbitrary binaries")
)

// ExecCheck grades a scenario by running an allowlisted command inside the
// seeded workspace. It is strictly opt-in: nothing in the eval harness invokes
// RunExecCheck on its own, so a fixture must name one explicitly.
//
// Security model: this is a LOCAL, DETERMINISTIC harness, NOT a sandbox. There
// is no OS isolation, no network restriction and no user switching. Safety
// comes from four bounds:
//
//  1. Validate must accept the command (go test/build or a relative .exe).
//  2. No shell is involved; argv is passed straight to os/exec.
//  3. Dir is always the disposable temp workspace, never the real repo.
//  4. The process is killed after a short, capped timeout.
//
// Treat a fixture that enables an exec check as trusted code, exactly like the
// Go test files a repository already runs. This is meant for pass@1-by-running
// on local, deterministic fixtures, not for grading untrusted submissions.
type ExecCheck struct {
	// Command is either "go" or a fixture-local relative path ending in .exe.
	Command string `json:"command"`
	// Args is passed verbatim to the process; for "go" it must start with
	// "test" or "build".
	Args []string `json:"args"`
	// TimeoutMS is the deadline in milliseconds: 0 means 30s, values above
	// 120000 are capped at 120s.
	TimeoutMS int `json:"timeout_ms"`
	// WantExitCode is the exit status that counts as a pass.
	WantExitCode int `json:"want_exit_code"`
	// WantStdoutContains, when set, must appear in the captured stdout tail.
	WantStdoutContains string `json:"want_stdout_contains,omitempty"`
}

// Validate rejects any command that is not explicitly allowlisted. It is
// called again by RunExecCheck, so a malformed check cannot bypass the bounds
// even if a caller forgets to validate first.
func (c ExecCheck) Validate() error {
	command := strings.TrimSpace(c.Command)
	if command == "" {
		return ErrExecEmptyCommand
	}
	if strings.ContainsAny(command, execShellMeta) {
		return fmt.Errorf("%w: command %q", ErrExecShellMeta, command)
	}

	if command == "go" {
		if len(c.Args) == 0 {
			return fmt.Errorf("%w: missing subcommand", ErrExecGoSubcommand)
		}
		switch c.Args[0] {
		case "test", "build":
		default:
			return fmt.Errorf("%w: %q", ErrExecGoSubcommand, c.Args[0])
		}
		return validateExecArgs(c.Args)
	}

	if err := validateFixtureExe(command); err != nil {
		return err
	}
	return validateExecArgs(c.Args)
}

// validateFixtureExe accepts only a relative path to a fixture-local .exe.
// Absolute paths (including Windows drive-relative ones), ".." traversal and
// unknown bare commands such as sh/bash/curl are all refused.
func validateFixtureExe(command string) error {
	if filepath.IsAbs(command) || strings.Contains(command, ":") {
		return fmt.Errorf("%w: %q", ErrExecBadPath, command)
	}
	for _, part := range strings.FieldsFunc(command, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return fmt.Errorf("%w: %q", ErrExecBadPath, command)
		}
	}
	if !strings.EqualFold(filepath.Ext(command), ".exe") {
		return fmt.Errorf("%w: %q (expected \"go\" or a relative .exe path)", ErrExecCommandDenied, command)
	}
	return nil
}

// validateExecArgs screens argv. Shell metacharacters are rejected even though
// no shell is used; -exec/-toolexec are refused because they let `go test` or
// `go build` launch an arbitrary binary, defeating the command allowlist.
func validateExecArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, execShellMeta) {
			return fmt.Errorf("%w: argument %q", ErrExecShellMeta, arg)
		}
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "-exec", "--exec", "-toolexec", "--toolexec":
			return fmt.Errorf("%w: %q", ErrExecDangerousGoFlag, arg)
		}
	}
	return nil
}

// ExecResult is the observed outcome of one exec check. StdoutTail and
// StderrTail hold at most the last 64KB of output, prefixed with a marker when
// truncated (the marker is kept on a rune boundary).
type ExecResult struct {
	ExitCode   int
	StdoutTail string
	StderrTail string
	TimedOut   bool
	Err        error
}

// RunExecCheck runs c inside workspace. The workspace is expected to be a
// disposable temp directory seeded by the eval harness; this function never
// changes directories itself and never starts a shell.
func RunExecCheck(workspace string, c ExecCheck) ExecResult {
	res := ExecResult{}
	if err := c.Validate(); err != nil {
		res.Err = err
		res.ExitCode = -1
		return res
	}

	deadline := defaultExecTimeout
	if c.TimeoutMS > 0 {
		deadline = time.Duration(c.TimeoutMS) * time.Millisecond
	}
	if deadline > maxExecTimeout {
		deadline = maxExecTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	// Resolve "go" to an absolute path in the parent's context BEFORE Dir is
	// applied. Without this, a seeded go.exe in the workspace could be picked
	// up by a relative PATH entry on Windows.
	name := c.Command
	if c.Command == "go" {
		if found, err := exec.LookPath("go"); err == nil {
			if abs, err := filepath.Abs(found); err == nil {
				name = abs
			}
		}
	}

	cmd := exec.CommandContext(ctx, name, c.Args...)
	cmd.Dir = workspace
	cmd.Env = execEnvironment()

	var stdout, stderr tailBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res.StdoutTail = stdout.String()
	res.StderrTail = stderr.String()
	res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	if err != nil {
		res.Err = err
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			// Start/chdir failure: the command never ran, so never grade it as
			// a successful exit 0.
			res.ExitCode = -1
		}
	}
	return res
}

// GradeExecCheck is a pure grading function: exit code must match and, when
// requested, stdout must contain the needle. A timeout or a failure to start
// is always a failure. A non-zero exit is fine when it is what the check wants.
func GradeExecCheck(res ExecResult, c ExecCheck) bool {
	if res.TimedOut {
		return false
	}
	if res.Err != nil {
		var exitErr *exec.ExitError
		if !errors.As(res.Err, &exitErr) {
			return false
		}
	}
	if res.ExitCode != c.WantExitCode {
		return false
	}
	return c.WantStdoutContains == "" || strings.Contains(res.StdoutTail, c.WantStdoutContains)
}

// execEnvironment drops GOFLAGS so a fixture cannot smuggle extra flags (for
// example -toolexec) into the allowlisted go command via the environment.
func execEnvironment() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// tailBuffer keeps only the last execOutputCap bytes written to it. Trimming
// advances to the next rune boundary so a tail never starts mid-UTF-8-rune.
type tailBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *tailBuffer) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.buf.Len() > execOutputCap {
		data := w.buf.Bytes()
		drop := w.buf.Len() - execOutputCap
		for drop < len(data) && !utf8.RuneStart(data[drop]) {
			drop++
		}
		copy(data, data[drop:])
		w.buf.Truncate(execOutputCap)
		w.truncated = true
	}
	return n, err
}

func (w *tailBuffer) String() string {
	if !w.truncated {
		return w.buf.String()
	}
	return execTruncMarker + w.buf.String()
}
