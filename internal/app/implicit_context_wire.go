package app

import (
	"example.com/argentgo/internal/session"
)

// 本文件提供"保留隐式上下文"的 app 侧装配骨架。它刻意保持零调用方：不读取进程环境、
// 不注册回调、不触碰压缩完成点。唯一职责是提供一个开关键谓词与一个纯函数，使后续接线
// 只需一行调用即可启用，而默认关闭时摘要逐字节不变。
//
// 机制（从被丢弃消息中提取尾部上下文与失败条目、合并进摘要）由 session 代理提供：
// session.ExtractImplicitContext 与 session.MergeImplicitIntoSummary。本文件只决定
// "是否启用"以及传给机制的默认策略常量，不重复实现任何提取或合并逻辑。

const (
	// implicitContextTailN 是从被丢弃消息中提取的尾部消息条数默认值。
	implicitContextTailN = 3
	// implicitContextFailN 是从被丢弃消息中提取的失败条目数默认值。
	implicitContextFailN = 2
)

// implicitContextEnabled 报告隐式上下文保留是否开启。reg 为 nil 时按默认关闭处理
// （fail-closed），因此零值注册表与未装配注册表的调用方都得不到任何行为变化。
func implicitContextEnabled(reg *OptInRegistry) bool {
	if reg == nil {
		return false
	}
	return reg.Enabled(OptInKeepImplicitContext)
}

// enrichWithImplicit 在开关开启时把被丢弃消息中的隐式上下文并入摘要，否则原样返回 summary。
//
// 关闭（reg 为 nil、开关未开启）时直接返回入参字符串，逐字节不变，因此默认运行的摘要
// 与事件流完全不受影响。开启时调用 session.ExtractImplicitContext 用默认 tailN/failN
// 常量提取隐式上下文，再交给 session.MergeImplicitIntoSummary 合并。
//
// 它是纯函数：只读入参、不修改 dropped 切片及其元素，也不读取任何全局状态；
// 相同的 summary/dropped/开关状态总得到相同的输出。
func enrichWithImplicit(summary string, dropped []session.Entry, reg *OptInRegistry) string {
	if !implicitContextEnabled(reg) {
		return summary
	}
	ic := session.ExtractImplicitContext(dropped, implicitContextTailN, implicitContextFailN)
	return session.MergeImplicitIntoSummary(summary, ic)
}
