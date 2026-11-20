//go:build windows

package proc

// terminateTree 是优雅取消边界的 Windows 侧实现。
// Windows 无法向进程组投递 SIGTERM，因此被取消的子进程
// 保持原有的立即终止行为：terminateTree 自身执行
// 终止，宽限期无需等待任何东西。
func (p *platformProcess) terminateTree() {
	p.killTree()
}
