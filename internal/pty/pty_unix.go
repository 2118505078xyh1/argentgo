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

// unixPty 是 /dev/ptmx 加上把它作为控制终端的子进程。子进程现在拥有自己的从端副本。Linux 在最后一个从描述符消失后报告 EIO。TIOCSWINSZ 使内核向前台组传递 SIGWINCH。Setsid 使子进程成为组长，因此 -pid 是其整棵树。
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
	// 复用 proc.SanitizeEnv 去掉宿主私有环境变量，避免嵌套 agent 误把
	// 父会话状态当成自己的；显式条目原样追加、优先级最高。
	child.Env = sanitizedEnv(cmd.Env)
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
	// 子进程现在拥有自己的从端副本。
	_ = slave.Close()

	return &unixPty{master: master, cmd: child, pid: child.Process.Pid, exitCode: -1}, nil
}

func (p *unixPty) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n, err := p.master.Read(b)
	if err != nil && n == 0 {
		// Linux 在最后一个从描述符消失后报告 EIO。
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
	// TIOCSWINSZ 使内核向前台组传递 SIGWINCH。
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
		// Setsid 使子进程成为组长，因此 -pid 是其整棵树。
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
		_ = syscall.Kill(p.pid, syscall.SIGKILL)
	}
	if p.master != nil {
		return p.master.Close()
	}
	return nil
}
