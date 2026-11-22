//go:build !windows

package proc

import (
	"os"
	"syscall"
)

func processAlive(p *os.Process) bool {
	// signal 0 不发送实际信号，只做权限/存在性检查。
	return syscall.Kill(p.Pid, 0) == nil
}
