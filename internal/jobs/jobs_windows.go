//go:build windows

package jobs

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"
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

// sameProcess reports whether pid is alive AND was created at startedAt. A pid
// alone is not an identity: once a job is reloaded from jobs.json the OS may
// have recycled the number for an unrelated process. GetProcessTimes gives the
// kernel's creation timestamp for that exact process object, so a recycled pid
// with a different creation time is rejected.
func sameProcess(pid int, startedAt time.Time) bool {
	if pid <= 0 || startedAt.IsZero() {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil || code != stillActive {
		return false
	}
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return false
	}
	createdAt := time.Unix(0, created.Nanoseconds())
	diff := createdAt.Sub(startedAt)
	if diff < 0 {
		diff = -diff
	}
	return diff <= identityTolerance
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
