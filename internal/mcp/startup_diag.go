package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

// startupReplayTimeout bounds one diagnostic replay. The shipped value is 2s;
// it is a var only so tests can shorten it. The replay is diagnostic-only: it
// must never be able to stall the caller's startup path.
var startupReplayTimeout = 2 * time.Second

// startupReplayGrace is a brief window after the deadline to collect a result
// from a run that was honouring the context, so its partial output is not lost
// to a race between ctx.Done() and the result channel.
const startupReplayGrace = 25 * time.Millisecond

// stderrDrainWait bounds how long stdio error enrichment waits for the async
// stderr drain goroutine to catch up with a server that just exited. A server
// that prints a startup error and dies closes stdout (waking readLoop) before
// the stderr drain is guaranteed to have run; a short bounded wait makes the
// diagnostic deterministic without ever stalling the caller's error path. A
// var so tests can shorten it.
var stderrDrainWait = 50 * time.Millisecond

// maxStartupReplayRunes caps how much child output is folded into one error
// line. A chatty server must not turn a failed startup into a huge log line.
const maxStartupReplayRunes = 4096

// startupOutcome is what one diagnostic replay produced.
type startupOutcome struct {
	out []byte
	err error
}

// EnrichStartupError turns an opaque stdio startup failure (a bare EOF or a
// dropped connection) into an error that also carries what the server command
// actually printed. The original error is always kept with %w, so callers that
// classify with errors.Is are unaffected.
//
// The replay is **diagnostic only** and deliberately not wired into Connect:
// starting a server twice has side effects, so the app layer must opt in by
// supplying run (typically exec.CommandContext(ctx, cmd, args...).CombinedOutput).
// With a nil run, a non-connection error, or an empty command, the error is
// returned unchanged — byte-for-byte identical error handling by default.
//
// run receives a context with a 2s deadline. The timeout is also enforced here
// with a select, so a run that ignores its context cannot stall EnrichStartupError;
// a timed-out replay is rewritten to "timed out after 2s". Keeping replay behind
// an injected function keeps this package stdlib-only and fully testable without
// spawning a process.
func EnrichStartupError(err error, cmd string, args []string, run func(context.Context, string, []string) ([]byte, error)) error {
	if !isStartupConnectionError(err) || run == nil || strings.TrimSpace(cmd) == "" {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), startupReplayTimeout)
	defer cancel()

	results := make(chan startupOutcome, 1)
	go func() {
		out, runErr := run(ctx, cmd, args)
		results <- startupOutcome{out: out, err: runErr}
	}()

	var outcome startupOutcome
	timedOut := false
	select {
	case outcome = <-results:
		// A well-behaved run returns its context error instead of making us
		// wait for the deadline; that is still a timeout, so report it as one.
		if errors.Is(outcome.err, context.DeadlineExceeded) || errors.Is(outcome.err, os.ErrDeadlineExceeded) {
			timedOut = true
		}
	case <-ctx.Done():
		timedOut = true
		// A run that honours its context returns right around the deadline.
		// Give it a brief grace window to hand over its partial output; a
		// run that ignores the context is bounded by this window too. The
		// channel is buffered, so the goroutine can never block on the send.
		select {
		case outcome = <-results:
		case <-time.After(startupReplayGrace):
		}
	}

	return enrichStartupOutcome(err, cmd, args, outcome, timedOut)
}

// isStartupConnectionError reports whether err is the kind of opaque failure
// worth replaying for diagnostics: a bare EOF (including errors.New("EOF")),
// a wrapped EOF, or a dropped/refused connection. It deliberately excludes
// ordinary errors so the vast majority of failures pass through untouched.
func isStartupConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	// A transport can flatten the cause to text: stdio.readLoop turns io.EOF
	// into "server closed the connection" without wrapping it, which is exactly
	// the bare-EOF report the audit called out. Match those phrasings too.
	text := strings.ToLower(err.Error())
	for _, needle := range []string{
		"eof",
		"connection closed",
		"closed the connection",
		"connection refused",
		"connection reset",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// enrichStartupOutcome folds one replay result into the original error. When
// the replay yielded no new information the original error is returned as-is.
func enrichStartupOutcome(err error, cmd string, args []string, outcome startupOutcome, timedOut bool) error {
	output := trimStartupOutput(outcome.out)
	label := startupCommandLabel(cmd, args)

	var detail string
	switch {
	case timedOut:
		detail = "timed out after " + startupReplayTimeout.String()
		if output != "" {
			detail += "; partial output: " + output
		}
	case output != "" && outcome.err != nil:
		detail = fmt.Sprintf("replay error: %v; output: %s", outcome.err, output)
	case output != "":
		detail = "output: " + output
	case outcome.err != nil:
		detail = "replay error: " + outcome.err.Error()
	default:
		return err
	}
	return fmt.Errorf("%w (startup diagnostic replay of %s: %s)", err, label, detail)
}

// appendStderrContext folds a server's captured stderr into an opaque startup
// connection error. It is a no-op unless err is the kind of bare EOF/dropped
// connection the audit called out and tail carries something useful, so the
// overwhelming majority of errors keep their byte-for-byte original text. The
// original error is always kept with %w, leaving errors.Is classification
// untouched.
func appendStderrContext(err error, tail string) error {
	if !isStartupConnectionError(err) {
		return err
	}
	trimmed := trimDiagnosticOutput([]byte(tail), "[server stderr truncated]")
	if trimmed == "" {
		return err
	}
	return fmt.Errorf("%w (server stderr: %s)", err, trimmed)
}

// enrichStderr attaches the stdio server's already-captured stderr to a
// connection error, with **no process replay and no side effects**. This is the
// in-package counterpart to EnrichStartupError: because newStdioTransport keeps
// a bounded stderr ring, the app's existing "print the error" path becomes
// self-sufficient — it needs neither a command re-run nor any code change.
// Non-stdio transports, non-connection errors and empty stderr leave err
// untouched.
func (c *client) enrichStderr(err error) error {
	if c == nil || !isStartupConnectionError(err) {
		return err
	}
	stdio, ok := c.tr.(*stdioTransport)
	if !ok {
		return err
	}
	return appendStderrContext(err, stdio.stderrWithDrain())
}

// trimStartupOutput normalises replay output for a single log line: trim outer
// whitespace and cap the length. Truncation runs on runes so a CJK string is
// never cut in the middle of a UTF-8 sequence.
func trimStartupOutput(out []byte) string {
	return trimDiagnosticOutput(out, "[startup replay output truncated]")
}

// trimDiagnosticOutput is trimStartupOutput with a caller-supplied truncation
// marker, so live-stderr enrichment can label its own truncation without
// changing the replay wording existing callers rely on.
func trimDiagnosticOutput(out []byte, marker string) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	if runes := []rune(s); len(runes) > maxStartupReplayRunes {
		s = string(runes[:maxStartupReplayRunes]) + "\n... " + marker + " ..."
	}
	return s
}

// startupCommandLabel renders the replayed command as one quoted string. %q
// keeps printable Unicode (e.g. CJK paths) readable.
func startupCommandLabel(cmd string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, cmd)
	parts = append(parts, args...)
	return fmt.Sprintf("%q", strings.Join(parts, " "))
}
