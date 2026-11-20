//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"syscall"
)

// 当调用方需要进程树终止时，configure 把子进程放进自己的进程组：
// 这样 kill(2) 中的负 pid 就能触及每个后代。
func configure(cmd *exec.Cmd, killTree, noWindow bool) {
	if killTree {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	_ = noWindow
}

// platformProcess 负责单个子进程的终止策略。
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
		// 子进程是它自己组的组长（Setpgid），因此负 pid 代表
		// 整棵子树。
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	_ = p.proc.Kill()
}

// terminateTree 用 SIGTERM 请求整个进程组停止，给协作式子进程
// 一个在 killTree 升级为 SIGKILL 之前清理的机会。
// 它模仿 killTree 的组/pid 目标选择，使每个后代都能收到
// 信号，而不只是直接子进程。
func (p *platformProcess) terminateTree() {
	if p.proc == nil {
		return
	}
	pid := p.proc.Pid
	if p.killAll && pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
}

func (p *platformProcess) close() {}

// configureDetached 把子进程放进自己的会话，使它能在启动它的
// shell 退出后继续存活，并且仍可按组终止。
func configureDetached(cmd *exec.Cmd, noWindow bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = noWindow
}

// shellCandidates 列出值得探测的 POSIX shell，最优者在前。
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
