//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"syscall"
)

// configure puts the child in its own process group when the caller wants tree
// kills: a negative pid in kill(2) then reaches every descendant.
func configure(cmd *exec.Cmd, killTree, noWindow bool) {
	if killTree {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	_ = noWindow
}

// platformProcess owns the kill strategy for one child.
type platformProcess struct {
	proc    *os.Process
	killAll bool
}

func newPlatformProcess(p *os.Process, killTree bool) *platformProcess {
	return &platformProcess{proc: p, killAll: killTree}
}

func (p *platformProcess) killTree() {
	if p.proc == nil {
		return
	}
	pid := p.proc.Pid
	if p.killAll && pid > 0 {
		// The child leads its own group (Setpgid), so the negative pid is the
		// whole subtree.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	_ = p.proc.Kill()
}

func (p *platformProcess) close() {}

// shellCandidates lists the POSIX shells worth probing, best first.
func shellCandidates() []string {
	var out []string
	if custom := os.Getenv("ARGENT_BASH"); custom != "" {
		out = append(out, custom)
	}
	if found, err := Which("bash"); err == nil {
		out = append(out, found)
	}
	out = append(out, "/bin/bash", "/usr/bin/bash", "/usr/local/bin/bash")
	if found, err := Which("sh"); err == nil {
		out = append(out, found)
	}
	out = append(out, "/bin/sh", "/usr/bin/sh")
	return out
}

func fallbackShell() (string, []string) {
	return "/bin/sh", []string{"-c"}
}
