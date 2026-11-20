//go:build !windows

package jobs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// detach 把作业放入自己的会话，使它能在启动它的 shell 之后
// 存活，并可被作为一组杀死。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// alive 报告 pid 是否为运行中的进程。
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	// EPERM 表示进程存在但属于另一用户。
	return errors.Is(err, syscall.EPERM)
}

// detach 给作业自己的进程组且无控制台，因此关闭启动它的终端不会把它带 down。alive 报告 pid 是否为运行中的进程。sameProcess 报告 pid 是否存活且在 startedAt 创建。单靠 pid 不是身份：作业从 jobs.json 重载后，OS 可能已把号码回收给无关进程。GetProcessTimes 给出那个确切进程对象的内核创建时间戳，因此创建时间不同的被回收 pid 会被拒绝。
// 单靠 pid 不是身份：作业从 jobs.json 重载后，OS 可能
// 已把该号码回收给无关进程。
//
// Linux 在 /proc/<pid>/stat 中导出以自启动以来的时钟滴答表示的进程启动时间，
// Linux 在 /proc/<pid>/stat（字段 22，USER_HZ = 100 ticks/s）中导出以自启动以来的时钟滴答表示的进程启动时间，并在 /proc/uptime 中导出自启动以来的挂钟时间。其他 Unix 系统没有 /proc，因此那里检查失败关闭：无法验证的作业被视为陈旧且从不被杀死。
// 挂钟时间在 /proc/uptime 中。其他 Unix 系统没有 /proc，因此检查
// 在那里失败关闭：无法验证的作业被视为陈旧且从不被杀死。
func sameProcess(pid int, startedAt time.Time) bool {
	if pid <= 0 || startedAt.IsZero() {
		return false
	}
	ticks, ok := processStartTicks(pid)
	if !ok {
		return false
	}
	uptime, ok := systemUptime()
	if !ok {
		return false
	}
	// 从读取 uptime 所用的同一"现在"推导启动时刻，然后
	// 加上进程自身自启动以来的偏移。两次读数相差
	// 从读取 uptime 所用的同一"现在"推导启动时刻，然后加上进程自身自启动以来的偏移。两次读数相差几毫秒；identityTolerance 吸收该偏差。
	created := time.Now().Add(-uptime).Add(time.Duration(ticks) * (time.Second / 100))
	diff := created.Sub(startedAt)
	if diff < 0 {
		diff = -diff
	}
	return diff <= identityTolerance
}

// processStartTicks 从 /proc/<pid>/stat 读取字段 22（starttime）。comm（字段 2）带括号且可能含空格或 ')'，因此最后一个 ')' 结束它。其后的字段从 state（字段 3）开始，使 starttime（字段 22）的索引为 19。
func processStartTicks(pid int) (uint64, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	// comm（字段 2）带括号且可能含空格或 ')'，因此
	// 最后一个 ')' 结束它。其后的字段从 state（字段 3）开始，使
	// starttime（字段 22）的索引为 19。
	raw := string(data)
	end := strings.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(raw[end+1:])
	if len(fields) <= 19 {
		return 0, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	return ticks, err == nil
}

// systemUptime 从 /proc/uptime 读取自启动以来的挂钟时间。killTree 终止 pid 及其所有后代。Setsid 使子进程成为会话领导者，因此 -pid 是其整个组。
func systemUptime() (time.Duration, bool) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// killTree 终止 pid 及其所有后代。
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	// Setsid 使子进程成为会话领导者，因此 -pid 是其整个组。
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
