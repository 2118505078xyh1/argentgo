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

// detach 给作业自己的进程组且无控制台，因此关闭
// 启动它的终端不会把它带 down。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
}

// alive 报告 pid 是否为运行中的进程。
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

// detach 给作业自己的进程组且无控制台，因此关闭启动它的终端不会把它带 down。alive 报告 pid 是否为运行中的进程。sameProcess 报告 pid 是否存活且在 startedAt 创建。单靠 pid 不是身份：作业从 jobs.json 重载后，OS 可能已把号码回收给无关进程。GetProcessTimes 给出那个确切进程对象的内核创建时间戳，因此创建时间不同的被回收 pid 会被拒绝。
// 单靠 pid 不是身份：作业从 jobs.json 重载后，OS 可能
// 已把号码回收给无关进程。GetProcessTimes 给出内核的
// 那个确切进程对象的创建时间戳，因此创建时间不同的被回收 pid
// 会被拒绝。
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

// killTree 终止 pid 及其所有后代。
//
// killTree 终止 pid 及其所有后代。分离 shell 的子进程在这里不在作业对象中，因此 taskkill /T 是触及整棵树的唯一方式。
// 是触及整棵树的唯一方式。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	child := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	_ = child.Run()
}
