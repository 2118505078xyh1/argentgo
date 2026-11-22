//go:build windows

package proc

import (
	"os"
	"syscall"
)

// processAlive 仅用于测试验证子进程是否已终止。
func processAlive(p *os.Process) bool {
	if p == nil || p.Pid <= 0 {
		return false
	}
	const (
		processQueryLimitedInformation = 0x1000
		stillActive                    = 259 // STILL_ACTIVE
	)
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(p.Pid))
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
