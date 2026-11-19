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
	"sync"
)

// maxLineBytes caps one newline-delimited JSON-RPC frame.
const maxLineBytes = 8 * 1024 * 1024

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
	cmd := exec.Command(spec.Command, spec.Args...)
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
		t.waitOnce.Do(func() { t.waitErr = t.cmd.Wait() })
	}
	var exitErr *exec.ExitError
	if t.waitErr != nil && !errors.As(t.waitErr, &exitErr) {
		return t.waitErr
	}
	return nil
}
