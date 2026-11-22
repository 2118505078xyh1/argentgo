package proc

import "os"

// ProcessAlive 报告进程是否仍在运行。
// 供需要在终止前判断目标是否存活的调用方使用：例如必须在父进程退出前
// 先杀整棵子树（Windows 的 taskkill /T 依赖父进程枚举后代）。
func ProcessAlive(p *os.Process) bool {
	if p == nil || p.Pid <= 0 {
		return false
	}
	return processAlive(p)
}
