package app

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

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	// verifyOutputCap bounds the captured stdout+stderr tail. Only the last
	// cap bytes survive, cut on a rune boundary so UTF-8 is never split.
	verifyOutputCap   = 32 * 1024
	verifyTruncMarker = "...[output truncated]..."
	verifyLabelMax    = 120

	// verifyShellMeta lists separators, redirections, substitutions and control
	// characters. No shell is used here; rejecting them is defence in depth so
	// a future refactor cannot accidentally make them live.
	verifyShellMeta = ";|&<>()$`'\";\n\r\x00^"

	defaultVerifyTimeoutMS = 30000
	maxVerifyTimeoutMS     = 120000
)

// Sentinel errors classify a rejected argv. They mirror the sentinels in
// internal/eval/execcheck.go so the two allowlists stay behaviourally aligned.
var (
	errVerifyEmptyCommand    = errors.New("app: verify: empty command")
	errVerifyCommandDenied   = errors.New("app: verify: command is not allowlisted")
	errVerifyGoSubcommand    = errors.New("app: verify: go subcommand must be test or build")
	errVerifyBadPath         = errors.New("app: verify: executable path must be relative and stay inside the workspace")
	errVerifyShellMeta       = errors.New("app: verify: shell metacharacters are not allowed")
	errVerifyDangerousGoFlag = errors.New("app: verify: go flag can run arbitrary binaries")
)

// Local, opt-in, security-bounded verification runner.
//
// This is NOT a sandbox: there is no OS isolation, no network restriction and
// no user switching. The operator explicitly opts in with a config key or
// --verify-cmd, and safety comes from boundedness rather than isolation:
//
//  1. The argv allowlist accepts only `go test|build ...` or a workspace-relative
//     .exe. Shells, curl, absolute paths, ".." traversal, shell metacharacters
//     and -exec/-toolexec are rejected (validateVerifyArgv).
//  2. argv is passed straight to os/exec; no shell is ever involved.
//  3. Dir is the workspace root, and GOFLAGS is stripped from the child env so
//     a smuggled -toolexec cannot reappear through the environment.
//  4. A short, capped context deadline kills a runaway process.
//  5. Captured output is a 32KB rune-safe tail, redacted before it is returned.
//
// Treat a configured command as trusted code, exactly like the Go tests a
// repository already runs. Nothing calls it unless Verify is set on the runner.

// validateVerifyArgv mirrors internal/eval/execcheck.go's ExecCheck.Validate.
// It is a local copy because that file belongs to another workstream; keep the
// two policies in sync when either changes.
//
// Allowed:
//   - "go" with argv[1] == "test" or "build"
//   - a relative path ending in .exe, with no ".." component
//
// Rejected: absolute paths (including Windows drive prefixes), bare commands
// such as sh/bash/curl, path traversal, shell metacharacters in any argument,
// and -exec/-toolexec variants that let go test/build launch arbitrary binaries.
func validateVerifyArgv(argv []string) error {
	if len(argv) == 0 {
		return errVerifyEmptyCommand
	}
	command := strings.TrimSpace(argv[0])
	if command == "" {
		return errVerifyEmptyCommand
	}
	if strings.ContainsAny(command, verifyShellMeta) {
		return fmt.Errorf("%w: command %q", errVerifyShellMeta, command)
	}
	args := argv[1:]

	if command == "go" {
		if len(args) == 0 {
			return fmt.Errorf("%w: missing subcommand", errVerifyGoSubcommand)
		}
		switch args[0] {
		case "test", "build":
		default:
			return fmt.Errorf("%w: %q", errVerifyGoSubcommand, args[0])
		}
		return validateVerifyArgs(args)
	}

	if filepath.IsAbs(command) || strings.Contains(command, ":") {
		return fmt.Errorf("%w: %q", errVerifyBadPath, command)
	}
	for _, part := range strings.FieldsFunc(command, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return fmt.Errorf("%w: %q", errVerifyBadPath, command)
		}
	}
	if !strings.EqualFold(filepath.Ext(command), ".exe") {
		return fmt.Errorf("%w: %q (expected \"go\" or a relative .exe path)", errVerifyCommandDenied, command)
	}
	return validateVerifyArgs(args)
}

func validateVerifyArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsAny(arg, verifyShellMeta) {
			return fmt.Errorf("%w: argument %q", errVerifyShellMeta, arg)
		}
		flag, _, _ := strings.Cut(arg, "=")
		switch flag {
		case "-exec", "--exec", "-toolexec", "--toolexec":
			return fmt.Errorf("%w: %q", errVerifyDangerousGoFlag, arg)
		}
	}
	return nil
}

// verifyEnvironment drops GOFLAGS so a configured `go test` cannot be given
// -toolexec (or another arbitrary-binary flag) through the environment.
func verifyEnvironment() []string {
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

// NewVerifyFunc builds the bounded verification callback consumed by the agent
// loop. cmd is the operator-configured argv; it is validated once here and the
// callback reuses the accepted copy. A misconfigured (empty or disallowed) cmd
// yields a callback that always reports ok=false, so no feedback is injected.
//
// changedPaths is deliberately ignored: the command is fixed and operator-
// controlled, and interpolating model-chosen paths into argv would reintroduce
// the injection this allowlist exists to prevent.
//
// On a completed process the callback returns (label, exit==0, redacted output,
// true). On spawn failure or timeout it returns (label, false, redacted error,
// true) so the loop can feed the failure back to the model.
func NewVerifyFunc(cmd []string, timeoutMS int, root string) func(context.Context, []string) (string, bool, string, bool) {
	argv := append([]string(nil), cmd...)
	label := verifyLabel(argv)

	if err := validateVerifyArgv(argv); err != nil {
		reason := llm.RedactText("verify command rejected: " + err.Error())
		return func(context.Context, []string) (string, bool, string, bool) {
			return label, false, reason, false
		}
	}

	if timeoutMS <= 0 {
		timeoutMS = defaultVerifyTimeoutMS
	}
	if timeoutMS > maxVerifyTimeoutMS {
		timeoutMS = maxVerifyTimeoutMS
	}
	deadline := time.Duration(timeoutMS) * time.Millisecond
	if strings.TrimSpace(root) == "" {
		root = "."
	}

	return func(ctx context.Context, _ []string) (string, bool, string, bool) {
		name := argv[0]
		if name == "go" {
			// Resolve "go" before Dir is applied so a go.exe planted in the
			// workspace cannot be picked up through a relative PATH entry.
			if found, err := exec.LookPath("go"); err == nil {
				if abs, err := filepath.Abs(found); err == nil {
					name = abs
				}
			}
		} else {
			// Validation guaranteed a relative, traversal-free .exe; anchoring
			// it to root keeps it inside the workspace even if Dir changes.
			name = filepath.Join(root, filepath.FromSlash(name))
		}

		runCtx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()

		command := exec.CommandContext(runCtx, name, argv[1:]...)
		command.Dir = root
		command.Env = verifyEnvironment()

		var out verifyTailBuffer
		command.Stdout = &out
		command.Stderr = &out
		err := command.Run()
		output := llm.RedactText(out.String())

		if err == nil {
			return label, true, output, true
		}

		// CommandContext kills a timed-out process, which surfaces as an
		// *exec.ExitError. Check the context first so a kill is reported as a
		// timeout rather than an ordinary non-zero exit.
		timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
		ctxErr := ctx.Err()
		if !timedOut && ctxErr == nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				// A normal non-zero exit is a failed check, still a valid verdict.
				return label, false, output, true
			}
		}

		// Spawn failure, deadline or cancellation: report a redacted error with
		// any partial output attached. ok stays true so the loop can react.
		detail := err.Error()
		if timedOut {
			detail = fmt.Sprintf("verify command timed out after %s", deadline)
		} else if ctxErr != nil {
			detail = "verify command cancelled: " + ctxErr.Error()
		}
		if tail := out.String(); tail != "" {
			detail += "\n" + tail
		}
		return label, false, llm.RedactText(detail), true
	}
}

// verifyLabel is a bounded, redacted name for the configured check.
func verifyLabel(argv []string) string {
	label := strings.Join(argv, " ")
	if runes := []rune(label); len(runes) > verifyLabelMax {
		label = string(runes[:verifyLabelMax]) + "..."
	}
	return llm.RedactText(label)
}

// verifyFuncFor returns nil unless the operator explicitly configured a
// verification command, so an unconfigured run's Options stay byte-identical
// (agent.Options.Verify == nil disables the whole loop).
func verifyFuncFor(res Resolved) func(context.Context, []string) (string, bool, string, bool) {
	if len(res.Cfg.VerifyCommand) == 0 {
		return nil
	}
	return NewVerifyFunc(res.Cfg.VerifyCommand, res.Cfg.VerifyTimeoutMS, res.Root)
}

// verifyReflectionsFor resolves the per-run cap. Zero is returned when the
// feature is off, so even this field matches the previous Options exactly.
func verifyReflectionsFor(res Resolved) int {
	if len(res.Cfg.VerifyCommand) == 0 {
		return 0
	}
	if n := res.Cfg.VerifyMaxReflections; n > 0 {
		return n
	}
	return agent.DefaultMaxVerifyReflections
}

// verifyTailBuffer keeps only the last verifyOutputCap bytes written to it,
// advancing to a rune boundary so a tail never starts mid-UTF-8-rune.
type verifyTailBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *verifyTailBuffer) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if w.buf.Len() > verifyOutputCap {
		data := w.buf.Bytes()
		drop := w.buf.Len() - verifyOutputCap
		for drop < len(data) && !utf8.RuneStart(data[drop]) {
			drop++
		}
		copy(data, data[drop:])
		w.buf.Truncate(verifyOutputCap)
		w.truncated = true
	}
	return n, err
}

func (w *verifyTailBuffer) String() string {
	if !w.truncated {
		return w.buf.String()
	}
	return verifyTruncMarker + w.buf.String()
}
