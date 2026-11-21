package security

import "sync/atomic"

// 本文件把原本只在测试里直接赋值的 PlanCommandGuard 字段变成一个生产可用的
// 计划模式开关。它只用标准库，不改动任何现有错误类型或错误文本：启用后复用
// chain.check 已有的拦截分支，拒绝字节与旧的 PlanCommandGuard=true 完全一致。

// EnablePlanMode 打开或关闭 Chain 的计划模式命令守卫。nil 接收者是无操作。
//
// 启用后，修改类 shell 命令（bash / powershell）会在策略 → 审批 → 审计链之前
// 被拒绝，返回 ErrRecoverablePlanBlock。agent 循环会把它当作可恢复的工具错误
// 交给模型，模型由此读到现有的 "blocked in plan mode" 提示并改用只读方式。
// 拒绝文案完全由 chain.check 生成，本方法不改变任何错误字节。
//
// 与直接写 PlanCommandGuard 字段不同，这里用原子标志，因而可在运行期被并发
// 调用（例如收到切换计划模式的指令时）而不会触发数据竞争。
func (c *Chain) EnablePlanMode(enabled bool) {
	if c == nil {
		return
	}
	var value uint32
	if enabled {
		value = 1
	}
	atomic.StoreUint32(&c.planMode, value)
}

// PlanModeEnabled 报告 c 当前是否启用了计划模式命令守卫。只读、并发安全，
// 供诊断使用。nil 接收者返回 false。
//
// 旧的 PlanCommandGuard 直接赋值同样计入，保证诊断结果与守卫的实际行为一致。
func PlanModeEnabled(c *Chain) bool {
	if c == nil {
		return false
	}
	return c.PlanCommandGuard || atomic.LoadUint32(&c.planMode) != 0
}
