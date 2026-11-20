package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
)

// pagerMinBytes is the output size at which an interactive pager starts to be
// worthwhile; shorter output should scroll by without an extra keystroke.
const pagerMinBytes = 4096

// shouldPage reports whether the optional pager path is warranted: a pager is
// configured, stdout is interactive, and the output is long enough to scroll.
// It is the decision hook for the pager, which is intentionally NOT spawned in
// this conservative pass (see writePaged). Keeping the decision as a pure
// function makes the non-TTY guarantee easy to test without a real console.
func shouldPage(pager, content string, isTTY bool) bool {
	return strings.TrimSpace(pager) != "" && isTTY && len(content) >= pagerMinBytes
}

// writePaged is the output gateway for long-form subcommands such as
// `sessions show` and `logs`. It writes content to w and treats a downstream
// reader closing early (e.g. `argent sessions show X | head`) as a normal stop
// rather than a command failure: broken-pipe errors are swallowed and nil is
// returned.
//
// The pager and isTTY arguments describe the optional paging path (see
// shouldPage): a later pass can pipe long output through $PAGER when stdout is
// a terminal. This conservative pass deliberately avoids spawning a
// subprocess (no new OS dependency), so content is always written directly
// and default stdout output is byte-for-byte unchanged. The arguments are
// accepted now so call sites do not need to change when paging is enabled.
func writePaged(w io.Writer, content string, pager string, isTTY bool) error {
	// Evaluate the optional paging decision but do not act on it yet: no pager
	// subprocess is spawned, so both outcomes fall through to the same write.
	_ = shouldPage(pager, content, isTTY)
	return writeIgnoringBrokenPipe(w, content)
}

// writeIgnoringBrokenPipe writes content to w, returning nil when the only
// failure is the consumer having closed the pipe.
func writeIgnoringBrokenPipe(w io.Writer, content string) error {
	_, err := io.WriteString(w, content)
	if isBrokenPipe(err) {
		return nil
	}
	return err
}

// isBrokenPipe recognises the errors produced when a downstream pipe or stdout
// handle is closed before all output has been consumed.
func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrClosed) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// On Windows a closed pipe surfaces as ERROR_BROKEN_PIPE, whose errno does
	// not map to the portable syscall.EPIPE sentinel; fall back to the message
	// so both `| head` and a closed os.File are covered there too.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "pipe has been ended") ||
		strings.Contains(msg, "pipe is being closed")
}

// writeLongOut routes a long-form command's output through writePaged, reading
// the optional pager from the environment and the TTY flag from the App seam.
// A nil HasTerminal (tests, embedded transports) means no terminal, which is
// exactly the conservative no-pager path.
func (a App) writeLongOut(content string) error {
	return writePaged(a.IO.Out, content, a.env("PAGER"), a.hasTerminal())
}

// reportLongOut writes content and maps only a real write failure to exit 1.
// A broken pipe is swallowed by writePaged, so `... | head` stays a success.
func reportLongOut(app App, content string) int {
	if err := app.writeLongOut(content); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	return 0
}
