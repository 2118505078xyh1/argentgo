//go:build !windows

package jobs

import (
	"errors"
	"os/exec"
	"syscall"
)

// detach puts the job in its own session so it survives the shell that started
// it and can be killed as a group.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// alive reports whether pid is a running process.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to another user.
	return errors.Is(err, syscall.EPERM)
}

// killTree terminates pid and every descendant.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	// Setsid made the child a session leader, so -pid is its whole group.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
