//go:build linux || darwin

package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

func supported() bool { return true }

// unixPty is /dev/ptmx plus the child that made it its controlling terminal.
type unixPty struct {
	master *os.File
	cmd    *exec.Cmd
	pid    int

	mu       sync.Mutex
	closed   bool
	reaped   bool
	exitCode int
}

func startPlatform(cmd Command, size Size) (terminal, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("pty: open /dev/ptmx: %w", err)
	}
	slaveName, err := unlockMaster(master.Fd())
	if err != nil {
		_ = master.Close()
		return nil, err
	}
	slave, err := os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, fmt.Errorf("pty: open %s: %w", slaveName, err)
	}
	if err := setWinsize(master.Fd(), size); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, err
	}

	child := exec.Command(cmd.Path, cmd.Args...)
	child.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		child.Env = append(os.Environ(), cmd.Env...)
	}
	child.Stdin = slave
	child.Stdout = slave
	child.Stderr = slave
	child.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    int(slave.Fd()),
	}
	if err := child.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, fmt.Errorf("pty: spawn %q: %w", cmd.Path, err)
	}
	// The child owns its own copy of the slave end now.
	_ = slave.Close()

	return &unixPty{master: master, cmd: child, pid: child.Process.Pid, exitCode: -1}, nil
}

func (p *unixPty) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n, err := p.master.Read(b)
	if err != nil && n == 0 {
		// Linux reports EIO once the last slave descriptor is gone.
		if errors.Is(err, syscall.EIO) || errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
	}
	return n, err
}

func (p *unixPty) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return p.master.Write(b)
}

func (p *unixPty) Resize(size Size) error {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return errors.New("pty: terminal is closed")
	}
	// TIOCSWINSZ makes the kernel deliver SIGWINCH to the foreground group.
	return setWinsize(p.master.Fd(), size)
}

func (p *unixPty) Wait() (int, error) {
	p.mu.Lock()
	if p.reaped {
		code := p.exitCode
		p.mu.Unlock()
		return code, nil
	}
	p.mu.Unlock()

	err := p.cmd.Wait()
	code := -1
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	}
	p.mu.Lock()
	p.reaped = true
	p.exitCode = code
	p.mu.Unlock()

	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return code, err
	}
	return code, nil
}

func (p *unixPty) Exited() bool {
	p.mu.Lock()
	reaped := p.reaped
	p.mu.Unlock()
	if reaped {
		return true
	}
	if p.pid > 0 {
		if err := syscall.Kill(p.pid, 0); err != nil && errors.Is(err, syscall.ESRCH) {
			return true
		}
	}
	return false
}

func (p *unixPty) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		return -1
	}
	return p.exitCode
}

func (p *unixPty) PID() int { return p.pid }

func (p *unixPty) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	if p.pid > 0 {
		// Setsid made the child a group leader, so -pid is its whole tree.
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		_ = syscall.Kill(p.pid, syscall.SIGKILL)
	}
	if p.master != nil {
		return p.master.Close()
	}
	return nil
}
