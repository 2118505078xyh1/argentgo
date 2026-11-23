//go:build !windows

package session

import "syscall"

// leasePIDAlive 用 signal 0 判断本机 PID 是否存活（不发送实际信号）。
// 与 internal/proc 的 processAlive 同构，但本包不反向依赖上层包。
func leasePIDAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
