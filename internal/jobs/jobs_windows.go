//go:build windows

package jobs

import (
	"os/exec"
	"strconv"
	"syscall"
)

const (
	detachedProcess       = 0x0000_0008
	createNewProcessGroup = 0x0000_0200
	createNoWindow        = 0x0800_0000

	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

// detach gives the job its own process group and no console, so closing the
// terminal that started it does not take it down.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
}

// alive reports whether pid is a running process.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

// killTree terminates pid and every descendant.
//
// A detached shell's children are not in a job object here, so taskkill /T is
// the only way to reach the whole tree.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	child := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	_ = child.Run()
}
