//go:build windows

package launcher

import (
	"os/exec"
	"syscall"
)

// createNoWindow keeps a console-launched CLI from flashing a console window
// when it spawns the browser.
const createNoWindow = 0x0800_0000

func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func defaultOpener() string { return "rundll32" }

func defaultOpenerArgs(url string) []string {
	return []string{"url.dll,FileProtocolHandler", url}
}
