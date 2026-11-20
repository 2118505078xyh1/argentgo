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
	done    chan struct{}

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
		name:    spec.Name,
		cmd:     cmd,
		stdin:   stdin,
		waiters: newWaiters(),
		diag:    &ringBuffer{limit: stderrRingBytes},
		done:    make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp %q: start %q: %w", spec.Name, spec.Command, err)
	}
	go t.readLoop(stdout)
	go t.drain(stderr)
	return t, nil
}

// readLoop turns stdout lines into correlated responses.
func (t *stdioTransport) readLoop(stdout io.Reader) {
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if len(line) > maxLineBytes {
				t.finish(fmt.Errorf("mcp %q: server sent a frame larger than %d bytes", t.name, maxLineBytes))
				return
			}
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
	_, _ = io.Copy(t.diag, stderr)
}

func (t *stdioTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.ids.take()
	ch := t.waiters.add(id)
	defer t.waiters.drop(id)

	frame, err := marshalRequest(id, method, params)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: encode %s: %w", t.name, method, err)
	}
	// Write on its own goroutine: a child that stops reading must not make the
	// call ignore its context. Closing stdin in finish unblocks the write.
	written := make(chan error, 1)
	go func() { written <- t.write(frame) }()
	select {
	case err := <-written:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		t.finish(fmt.Errorf("mcp %q: %s cancelled: %w", t.name, method, ctx.Err()))
		return nil, fmt.Errorf("mcp %q: %s: %w", t.name, method, ctx.Err())
	case <-t.done:
		return nil, t.closedError(method)
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
	return t.write(frame)
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
