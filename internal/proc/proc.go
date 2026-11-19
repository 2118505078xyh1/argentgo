// Package proc runs child processes with a timeout, a bounded capture of
// stdout/stderr and whole-tree kills.
//
// It is the Go port of argent's core process helpers: every tool that shells
// out goes through Run, so the timeout, the output cap and the tree kill are
// enforced in exactly one place.
package proc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultMaxOutput is the default cap, per stream, on captured output.
const DefaultMaxOutput = 64 << 10

// MaxOutputBytes caps each captured stream. Both the head and the tail of a
// stream are kept: a failure message at the end of a noisy build is the part
// that matters. Tests lower it.
var MaxOutputBytes = DefaultMaxOutput

const truncatedMarker = "\n... [output truncated] ...\n"

// Command describes a child process to run.
type Command struct {
	// Path is the program to execute; it is never resolved through a shell.
	Path string
	// Args are passed to Path unchanged.
	Args []string
	// Dir is the working directory; empty keeps the current one.
	Dir string
	// Env entries ("K=V") are appended to the inherited environment.
	Env []string
	// Stdin, when set, is fed to the child and then closed.
	Stdin io.Reader
	// Timeout kills the whole tree once it elapses. Zero waits forever.
	Timeout time.Duration
	// KillTree kills the child's descendants along with the child.
	KillTree bool
	// KeepOnTimeout leaves a timed-out child running in its own session instead
	// of killing it. Only RunWithTimeout honours this; the returned
	// *TimeoutError.Live is the still-running child the caller must adopt.
	KeepOnTimeout bool
	// NoWindow hides the console window the child would otherwise open.
	// Windows only; ignored elsewhere.
	NoWindow bool
}

// Result is what a finished child produced.
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	Duration  time.Duration
}

// Success reports a clean exit.
func (r Result) Success() bool { return r.ExitCode == 0 }

// Combined is stdout followed by stderr, in the order they were captured.
func (r Result) Combined() string {
	if r.Stderr == "" {
		return r.Stdout
	}
	if r.Stdout == "" {
		return r.Stderr
	}
	return r.Stdout + r.Stderr
}

// CancelReason describes why a run was cut short. It is a purely additive
// classification helper: Run keeps wrapping context errors with %w exactly as
// before, so callers that already use errors.Is are unaffected. ClassifyCancel
// lets a caller that only needs the reason avoid reimplementing that chain.
type CancelReason uint8

const (
	// CancelNone means there was no error to classify.
	CancelNone CancelReason = iota
	// CancelTimeout means the error wraps context.DeadlineExceeded.
	CancelTimeout
	// CancelUser means the error wraps context.Canceled, i.e. an explicit
	// interrupt rather than an elapsed deadline.
	CancelUser
	// CancelSignal means the run was interrupted by an OS signal. It is part of
	// the reason vocabulary for callers that wrap signal cancellations in their
	// own sentinel; ClassifyCancel never returns it on its own.
	CancelSignal
	// CancelUnknown means the error is non-nil but carries no known cause.
	CancelUnknown
)

// ClassifyCancel maps err to a CancelReason using errors.Is, so wrapped errors
// such as the ones Run returns are classified by their cause. A nil error is
// CancelNone; an error that matches no known cause is CancelUnknown.
func ClassifyCancel(err error) CancelReason {
	if err == nil {
		return CancelNone
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return CancelTimeout
	case errors.Is(err, context.Canceled):
		return CancelUser
	default:
		return CancelUnknown
	}
}

// String implements fmt.Stringer, giving each reason a short stable label.
func (r CancelReason) String() string {
	switch r {
	case CancelNone:
		return "none"
	case CancelTimeout:
		return "timeout"
	case CancelUser:
		return "user"
	case CancelSignal:
		return "signal"
	case CancelUnknown:
		return "unknown"
	default:
		return fmt.Sprintf("CancelReason(%d)", uint8(r))
	}
}

// Run executes c and returns its captured output.
//
// A non-zero exit is not an error: the exit code is in the Result. An error is
// only returned when the child could not be started, could not be reaped, or
// the run was cut short by ctx (the error wraps ctx.Err()) or by Command.Timeout
// (the error wraps context.DeadlineExceeded). Both of those still kill the
// whole tree and still fill in the Result.
func Run(ctx context.Context, c Command) (Result, error) {
	started := time.Now()
	if c.Path == "" {
		return Result{ExitCode: -1}, errors.New("proc: empty command path")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	out := &cappedBuffer{max: MaxOutputBytes}
	errOut := &cappedBuffer{max: MaxOutputBytes}

	cmd := exec.Command(c.Path, c.Args...)
	cmd.Dir = c.Dir
	// Always sanitize: even with no per-call env, the child must not inherit
	// host-private session plumbing variables (nested agents rely on this).
	cmd.Env = SanitizeEnv(c.Env)
	cmd.Stdout = out
	cmd.Stderr = errOut
	configure(cmd, c.KillTree, c.NoWindow)

	// Feed stdin from a pipe we own: exec's own stdin copying would be waited
	// on by Wait, so a reader that never returns could hang the whole call.
	var stdinRead *os.File
	if c.Stdin != nil {
		pr, pw, err := os.Pipe()
		if err != nil {
			return Result{ExitCode: -1}, fmt.Errorf("proc: stdin pipe: %w", err)
		}
		cmd.Stdin = pr
		stdinRead = pr
		go func() {
			_, _ = io.Copy(pw, c.Stdin)
			_ = pw.Close()
		}()
	}

	if err := cmd.Start(); err != nil {
		if stdinRead != nil {
			_ = stdinRead.Close()
		}
		return Result{ExitCode: -1}, fmt.Errorf("proc: spawn %q: %w", c.Path, err)
	}
	// The child has its own handle now; closing ours is what turns the write
	// end of the pipe into an EOF for it once the copier finishes.
	if stdinRead != nil {
		_ = stdinRead.Close()
	}

	handle := newPlatformProcess(cmd.Process, c.KillTree)
	defer handle.close()

	done := make(chan waitOutcome, 1)
	go func() {
		err := cmd.Wait()
		done <- waitOutcome{err: err, state: cmd.ProcessState}
	}()

	var timeout <-chan time.Time
	if c.Timeout > 0 {
		timer := time.NewTimer(c.Timeout)
		defer timer.Stop()
		timeout = timer.C
	}

	var (
		got      waitOutcome
		timedOut bool
		ctxErr   error
	)
	select {
	case got = <-done:
	case <-timeout:
		timedOut = true
		handle.killTree()
		got = awaitOutcome(done)
	case <-ctx.Done():
		ctxErr = ctx.Err()
		handle.killTree()
		got = awaitOutcome(done)
	}

	res := Result{
		Stdout:    out.String(),
		Stderr:    errOut.String(),
		ExitCode:  -1,
		Truncated: out.dropped || errOut.dropped,
		Duration:  time.Since(started),
	}
	if got.state != nil {
		res.ExitCode = got.state.ExitCode()
	}

	switch {
	case timedOut:
		return res, fmt.Errorf("proc: %q exceeded %s: %w", c.Path, c.Timeout, context.DeadlineExceeded)
	case ctxErr != nil:
		return res, fmt.Errorf("proc: %q: %w", c.Path, ctxErr)
	case got.err != nil && !isExitError(got.err):
		return res, fmt.Errorf("proc: wait %q: %w", c.Path, got.err)
	}
	return res, nil
}

// waitOutcome carries everything Wait learned, so the caller never reads
// cmd.ProcessState from another goroutine.
type waitOutcome struct {
	err   error
	state *os.ProcessState
}

// awaitOutcome gives a killed child a moment to be reaped. A tree that leaks a
// handle into a grandchild must not hang the caller forever.
func awaitOutcome(done <-chan waitOutcome) waitOutcome {
	select {
	case o := <-done:
		return o
	case <-time.After(5 * time.Second):
		return waitOutcome{}
	}
}

func isExitError(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit)
}

// cappedBuffer keeps the first half and the last half of a stream.
type cappedBuffer struct {
	max     int
	head    []byte
	tail    []byte
	seen    int
	dropped bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.seen += n
	if b.max <= 0 {
		if n > 0 {
			b.dropped = true
		}
		return n, nil
	}
	headCap := (b.max + 1) / 2
	tailCap := b.max - headCap
	if room := headCap - len(b.head); room > 0 && len(p) > 0 {
		take := room
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	if len(p) > 0 {
		b.tail = append(b.tail, p...)
		if len(b.tail) > tailCap {
			b.dropped = true
			if tailCap == 0 {
				b.tail = b.tail[:0]
			} else {
				copy(b.tail, b.tail[len(b.tail)-tailCap:])
				b.tail = b.tail[:tailCap]
			}
		}
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	if !b.dropped {
		return string(b.head) + string(b.tail)
	}
	return string(b.head) + truncatedMarker + string(b.tail)
}

// Which resolves a program name against PATH.
func Which(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("proc: empty program name")
	}
	found, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("proc: %q not found on PATH", name)
	}
	if abs, err := filepath.Abs(found); err == nil {
		found = abs
	}
	return found, nil
}

var (
	shellOnce sync.Once
	shellPath string
	shellArgs []string
)

// Shell builds a Command that runs command through a POSIX shell.
//
// bash is preferred (Git Bash on Windows, which is what the tools expect) and
// it is only used when it actually runs: a bash.exe left on PATH by the WSL
// app-execution alias exits with an error when no distribution is installed, so
// the candidate has to answer a probe. Without one, cmd.exe is used on Windows
// and /bin/sh elsewhere.
func Shell(command string) Command {
	path, args := locateShell()
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, args...)
	argv = append(argv, command)
	return Command{
		Path:     path,
		Args:     argv,
		NoWindow: runtime.GOOS == "windows",
	}
}

func locateShell() (string, []string) {
	shellOnce.Do(func() {
		if path, ok := firstWorkingShell(shellCandidates()); ok {
			shellPath, shellArgs = path, shellArgsFor(path)
			return
		}
		shellPath, shellArgs = fallbackShell()
	})
	return shellPath, shellArgs
}

// firstWorkingShell returns the first candidate that proves it can run.
func firstWorkingShell(candidates []string) (string, bool) {
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if probeShell(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func probeShell(path string) bool {
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Run(ctx, Command{
		Path:     path,
		Args:     []string{"-c", "exit 0"},
		Timeout:  10 * time.Second,
		NoWindow: true,
	})
	return err == nil && res.ExitCode == 0
}

func shellArgsFor(path string) []string {
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "bash"):
		if runtime.GOOS == "windows" {
			// -l picks up the profile, and with it the PATH Git Bash expects.
			return []string{"-lc"}
		}
		return []string{"-c"}
	case base == "cmd.exe" || base == "cmd":
		return []string{"/C"}
	default:
		return []string{"-c"}
	}
}
