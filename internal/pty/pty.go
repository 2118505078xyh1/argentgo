// Package pty runs a child process attached to a pseudoterminal.
//
// A program that checks isatty and then behaves interactively — a dev server, a
// REPL, a test runner in watch mode — needs a terminal; behind a plain pipe it
// thinks it is writing to a file and stops colouring, prompting or flushing.
// Windows uses ConPTY (CreatePseudoConsole), unix opens /dev/ptmx, both through
// raw syscalls so the package stays free of cgo.
package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrUnsupported is returned when this platform cannot create a terminal.
var ErrUnsupported = errors.New("pty: pseudoterminals are not supported on this platform")

// Size is a terminal size in character cells.
type Size struct {
	Cols uint16
	Rows uint16
}

// DefaultSize is the size used when the caller passes a zero Size.
var DefaultSize = Size{Cols: 120, Rows: 40}

// Command is the child process to attach to the terminal.
type Command struct {
	// Path is the program to execute; it is never resolved through a shell.
	Path string
	// Args are passed to Path unchanged.
	Args []string
	// Dir is the working directory; empty keeps the current one.
	Dir string
	// Env entries ("K=V") are appended to the inherited environment.
	Env []string
}

// terminal is the platform half of a session.
type terminal interface {
	io.ReadWriteCloser
	// Resize changes the window size reported to the child.
	Resize(Size) error
	// Wait blocks until the child exits and returns its exit code.
	Wait() (int, error)
	// Exited reports whether the child has already exited.
	Exited() bool
	// ExitCode is the exit code, or -1 while the child runs.
	ExitCode() int
	// PID is the child's process id.
	PID() int
}

// Pty is one running process attached to a pseudoterminal.
type Pty struct {
	term terminal
	// sizeMu guards size: Resize may run on a UI/window event while another
	// goroutine reads Size for rendering, and two concurrent Resizes must not
	// leave size disagreeing with the size actually applied to the terminal.
	sizeMu sync.RWMutex
	size   Size

	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}

	waitOnce sync.Once
	waitCode int
	waitErr  error
}

// Supported reports whether this build can create a pseudoterminal.
func Supported() bool { return supported() }

// Start launches cmd inside a new pseudoterminal of the given size.
//
// Cancelling ctx kills the child and closes the terminal.
func Start(ctx context.Context, cmd Command, size Size) (*Pty, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if cmd.Path == "" {
		return nil, errors.New("pty: empty command path")
	}
	if !supported() {
		return nil, ErrUnsupported
	}
	if size.Cols < 2 || size.Rows < 2 {
		size = DefaultSize
	}
	term, err := startPlatform(cmd, size)
	if err != nil {
		return nil, err
	}
	p := &Pty{term: term, size: size, closed: make(chan struct{})}
	if done := ctx.Done(); done != nil {
		go func() {
			select {
			case <-done:
				_ = p.Close()
			case <-p.closed:
			}
		}()
	}
	return p, nil
}

// Read reads whatever the terminal produced next.
//
// It returns io.EOF once the child exited and the stream is drained. On Windows
// the console holds its end of the output pipe open until Wait reaps the child
// and releases the console (or until Close), so EOF follows Wait/Close there.
func (p *Pty) Read(b []byte) (int, error) { return p.term.Read(b) }

// Write sends input to the terminal.
func (p *Pty) Write(b []byte) (int, error) { return p.term.Write(b) }

// Resize changes the window size; the child is signalled with SIGWINCH on unix.
func (p *Pty) Resize(size Size) error {
	if size.Cols < 2 || size.Rows < 2 {
		return fmt.Errorf("pty: refusing a %dx%d size", size.Cols, size.Rows)
	}
	// Hold the write lock across the platform call so concurrent Resizes are
	// serialised: the reported size always matches the last one applied to the
	// terminal, and Size() can never observe a half-updated value.
	p.sizeMu.Lock()
	defer p.sizeMu.Unlock()
	if err := p.term.Resize(size); err != nil {
		return err
	}
	p.size = size
	return nil
}

// Size is the terminal size last requested.
func (p *Pty) Size() Size {
	p.sizeMu.RLock()
	defer p.sizeMu.RUnlock()
	return p.size
}

// Close terminates the child and releases the terminal. It is idempotent.
func (p *Pty) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.closeErr = p.term.Close()
	})
	return p.closeErr
}

// Wait blocks until the child exits and returns its exit code.
func (p *Pty) Wait() (int, error) {
	p.waitOnce.Do(func() {
		p.waitCode, p.waitErr = p.term.Wait()
	})
	return p.waitCode, p.waitErr
}

// Exited reports whether the child has already exited.
func (p *Pty) Exited() bool { return p.term.Exited() }

// ExitCode is the child's exit code, or -1 while it runs.
func (p *Pty) ExitCode() int { return p.term.ExitCode() }

// PID is the child's process id.
func (p *Pty) PID() int { return p.term.PID() }
