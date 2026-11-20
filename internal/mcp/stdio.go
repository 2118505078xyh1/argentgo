package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

// maxLineBytes caps one newline-delimited JSON-RPC frame.
const maxLineBytes = 8 * 1024 * 1024

// execSpec resolves a stdio server's command into the executable name and
// arguments to hand to exec.Command.
//
// On Windows, PowerShell scripts (.ps1) cannot be launched directly:
// CreateProcess rejects them ("%1 is not a valid Win32 application"), and
// Go's LookPath never finds them because .PS1 is not in PATHEXT. Node tools
// such as npx are sometimes installed only as npx.ps1 (no .cmd shim), and a
// server configured as a local .ps1 script hits the same wall. Run those
// through PowerShell instead. Ordinary commands and the usual npx.cmd shim
// are left untouched because Go already executes them safely.
func execSpec(spec Spec) (string, []string) {
	return execSpecForOS(runtime.GOOS, spec.Command, spec.Args)
}

// execSpecForOS is the testable core of execSpec. goos is a parameter so the
// Windows wrapping rules can be exercised on any host.
func execSpecForOS(goos, command string, args []string) (string, []string) {
	if goos != "windows" {
		return command, args
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return command, args
	}
	// An explicit .ps1 path is never a valid Win32 executable.
	if strings.EqualFold(filepath.Ext(command), ".ps1") {
		return powershellScript(command, args)
	}
	if resolved, err := exec.LookPath(command); err == nil {
		// LookPath may hand back a .ps1 when the user added .PS1 to PATHEXT.
		if strings.EqualFold(filepath.Ext(resolved), ".ps1") {
			return powershellScript(resolved, args)
		}
		// A normal executable or the standard npx.cmd shim needs no wrapper.
		return command, args
	}
	// Bare name that Go cannot find: a .ps1-only install (e.g. npx.ps1).
	if filepath.Base(command) == command {
		if script := findPS1OnPath(command); script != "" {
			return powershellScript(script, args)
		}
	}
	return command, args
}

// powershellScript builds an invocation that runs a .ps1 file. Execution policy
// is bypassed because the script is an explicitly configured server command;
// -NoProfile keeps the user's profile from changing the child's environment.
func powershellScript(script string, args []string) (string, []string) {
	wrapped := make([]string, 0, len(args)+5)
	wrapped = append(wrapped, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
	wrapped = append(wrapped, args...)
	return "powershell.exe", wrapped
}

// findPS1OnPath returns the absolute path of <name>.ps1 on PATH, or "". Go's
// LookPath ignores .ps1, so this fills the one gap that matters for Windows
// script-based MCP servers.
func findPS1OnPath(name string) string {
	if filepath.Ext(name) != "" {
		return ""
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name+".ps1")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// stdioTransport speaks newline-delimited JSON-RPC over a child's pipes.
type stdioTransport struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	ids     idSequence
	waiters *waiters
	diag    *ringBuffer
	// drainDone closes when the stderr drain goroutine has copied everything
	// the child wrote. Error enrichment waits on it (briefly) so a startup
	// diagnostic printed just before the child exits is not lost to a race.
	drainDone chan struct{}
	done      chan struct{}

	closeOnce sync.Once
	waitOnce  sync.Once
	waitErr   error

	mu      sync.Mutex
	doneErr error
}

func newStdioTransport(spec Spec) (*stdioTransport, error) {
	name, args := execSpec(spec)
	cmd := exec.Command(name, args...)
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %q: stdin pipe: %w", spec.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %q: stdout pipe: %w", spec.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %q: stderr pipe: %w", spec.Name, err)
	}

	t := &stdioTransport{
		name:      spec.Name,
		cmd:       cmd,
		stdin:     stdin,
		waiters:   newWaiters(),
		diag:      &ringBuffer{limit: stderrRingBytes},
		drainDone: make(chan struct{}),
		done:      make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %q: start %q: %w", spec.Name, spec.Command, err)
	}
	go t.readLoop(stdout)
	go t.drain(stderr)
	return t, nil
}

// errLineTooLong is returned by readBoundedLine once a frame crosses the cap.
var errLineTooLong = errors.New("mcp: frame too long")

// readBoundedLine reads one newline-delimited frame from reader, accumulating
// at most limit bytes. bufio.Reader.ReadBytes keeps growing its result until a
// newline (or EOF) arrives, so a server that emits a giant line with no newline
// would otherwise allocate without bound and defeat maxLineBytes. ReadSlice
// only ever returns bytes already inside the reader's fixed buffer and reports
// bufio.ErrBufferFull when more remain, which lets us enforce the cap as we go.
func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > limit {
			return line, errLineTooLong
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

// readLoop turns stdout lines into correlated responses.
func (t *stdioTransport) readLoop(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := readBoundedLine(reader, maxLineBytes)
		if errors.Is(err, errLineTooLong) {
			t.finish(fmt.Errorf("mcp %q: server sent a frame larger than %d bytes", t.name, maxLineBytes))
			return
		}
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				// Invalid lines are tolerated: a server may print logs on stdout.
				_ = t.waiters.handle(trimmed)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.finish(fmt.Errorf("mcp %q: server closed the connection", t.name))
			} else {
				t.finish(fmt.Errorf("mcp %q: read from server failed: %w", t.name, err))
			}
			return
		}
	}
}

// drain keeps the child's stderr in a bounded buffer for diagnostics.
func (t *stdioTransport) drain(stderr io.Reader) {
	defer close(t.drainDone)
	_, _ = io.Copy(t.diag, stderr)
}

// stderrWithDrain returns the captured stderr tail, waiting briefly for the
// async drain goroutine to finish first. A server that exits right after
// printing a startup error closes stdout (waking readLoop) before the stderr
// drain is guaranteed to have run; the bounded wait makes the diagnostic
// deterministic without ever stalling the caller's error path. Transports built
// by hand in tests have no drain channel and simply return what is captured so
// far.
func (t *stdioTransport) stderrWithDrain() string {
	if t == nil {
		return ""
	}
	if t.drainDone != nil {
		select {
		case <-t.drainDone:
		case <-time.After(stderrDrainWait):
		}
	}
	if t.diag == nil {
		return ""
	}
	return t.diag.String()
}

func (t *stdioTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.ids.take()
	ch := t.waiters.add(id)
	defer t.waiters.drop(id)

	frame, err := marshalRequest(id, method, params)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: encode %s: %w", t.name, method, err)
	}
	if err := t.writeContext(ctx, method, frame); err != nil {
		return nil, err
	}

	select {
	case reply := <-ch:
		return reply.result, reply.err
	case <-ctx.Done():
		// Cancelling a call kills the child: a stdio server has no other way
		// to be interrupted, and leaving it running would leak the process.
		t.finish(fmt.Errorf("mcp %q: %s cancelled: %w", t.name, method, ctx.Err()))
		return nil, fmt.Errorf("mcp %q: %s: %w", t.name, method, ctx.Err())
	case <-t.done:
		return nil, t.closedError(method)
	}
}

func (t *stdioTransport) notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mcp %q: %s: %w", t.name, method, err)
	}
	frame, err := marshalNotification(method, params)
	if err != nil {
		return fmt.Errorf("mcp %q: encode %s: %w", t.name, method, err)
	}
	return t.writeContext(ctx, method, frame)
}

// writeContext writes one frame without letting a child that stopped reading
// its stdin hold the caller past its deadline. A pipe write can block forever
// once the child's buffer fills, so it runs on its own goroutine and the
// context (or a closed transport) wins the race. finish closes stdin, which
// unblocks a write already in flight so the goroutine cannot leak.
func (t *stdioTransport) writeContext(ctx context.Context, method string, frame []byte) error {
	written := make(chan error, 1)
	go func() { written <- t.write(frame) }()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		t.finish(fmt.Errorf("mcp %q: %s cancelled: %w", t.name, method, ctx.Err()))
		return fmt.Errorf("mcp %q: %s: %w", t.name, method, ctx.Err())
	case <-t.done:
		return t.closedError(method)
	}
}

func (t *stdioTransport) write(frame []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	select {
	case <-t.done:
		return t.closedError("write")
	default:
	}
	frame = append(frame, '\n')
	if _, err := t.stdin.Write(frame); err != nil {
		return fmt.Errorf("mcp %q: write to server failed: %w", t.name, err)
	}
	return nil
}

// reap waits on the child exactly once and records the result. It must not run
// on readLoop or drain: cmd.Wait closes the pipes those goroutines read from,
// so calling it there could deadlock against itself.
func (t *stdioTransport) reap() {
	t.waitOnce.Do(func() { t.waitErr = t.cmd.Wait() })
}

// finish records why the connection ended and wakes every waiter.
func (t *stdioTransport) finish(err error) {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.doneErr = err
		t.mu.Unlock()
		close(t.done)
		t.waiters.failAll(err)
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
			// Killing is not enough: without a Wait the process stays unreaped
			// (a zombie on Unix, a leaked handle on Windows). Reap it in the
			// background so a call timeout never leaves the child behind.
			go t.reap()
		}
		_ = t.stdin.Close()
	})
}

func (t *stdioTransport) closedError(method string) error {
	t.mu.Lock()
	err := t.doneErr
	t.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("mcp %q: connection closed", t.name)
	}
	if method == "" {
		return err
	}
	return fmt.Errorf("%w (during %s)", err, method)
}

// Close kills the child and reaps it.
func (t *stdioTransport) Close() error {
	t.finish(fmt.Errorf("mcp %q: client closed", t.name))
	if t.cmd.Process != nil {
		t.reap()
	}
	var exitErr *exec.ExitError
	if t.waitErr != nil && !errors.As(t.waitErr, &exitErr) {
		return t.waitErr
	}
	return nil
}
