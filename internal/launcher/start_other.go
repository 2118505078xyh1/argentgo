//go:build !windows

package launcher

import (
	"os/exec"
	"runtime"
	"syscall"
)

// prepare gives the browser its own process group so terminal signals aimed
// at the CLI do not reach the detached app window.
func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func defaultOpener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func defaultOpenerArgs(url string) []string { return []string{url} }
