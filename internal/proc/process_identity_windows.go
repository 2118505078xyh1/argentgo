//go:build windows

package proc

import (
	"fmt"
	"syscall"
	"time"
)

// processQueryLimitedInformation is PROCESS_QUERY_LIMITED_INFORMATION. It is
// enough for GetProcessTimes and, unlike full query rights, still works for
// protected processes.
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
	// GetProcessTimes returns CreateTime as a FILETIME (100ns since 1601);
	// Filetime.Nanoseconds rebases it to the Unix epoch. Millisecond identity
	// values match the Unix probe's units and the tolerance's unit.
	return created.Nanoseconds() / int64(time.Millisecond), nil
}
