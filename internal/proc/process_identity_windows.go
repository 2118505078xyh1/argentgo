//go:build windows

package proc

import (
	"fmt"
	"syscall"
	"time"
)

// processQueryLimitedInformation 即 PROCESS_QUERY_LIMITED_INFORMATION。它
// 足以调用 GetProcessTimes，而且与完全查询权限不同，它仍适用于
// 受保护的进程。
const processQueryLimitedInformation = 0x1000

func processStartTime(pid int) (int64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("proc: invalid pid %d", pid)
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("proc: open pid %d: %w", pid, err)
	}
	defer syscall.CloseHandle(handle)

	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return 0, fmt.Errorf("proc: query times for pid %d: %w", pid, err)
	}
	// GetProcessTimes 以 FILETIME（自 1601 起 100ns）返回 CreateTime；
	// Filetime.Nanoseconds 把它换算到 Unix epoch。毫秒级身份
	// 值与 Unix 探测的单位及容差的单位一致。
	return created.Nanoseconds() / int64(time.Millisecond), nil
}
