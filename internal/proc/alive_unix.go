//go:build !windows

package proc

import (
	"os"
	"syscall"
)

// processAlive 仅用于测试验证子进程是否已终止（signal 0 不发送实际信号）。
func processAlive(p *os.Process) bool {
	return p != nil && p.Pid > 0 && syscall.Kill(p.Pid, 0) == nil
}
