package app

import (
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// 本文件提供"压缩摘要关键章节保留"的 app 侧装配骨架。它刻意保持零调用方：不读取进程
// 环境、不注册回调、不触碰 buildCompactionSummary（那是后续接线的工作）。唯一职责是提供
// 开关判定与一个纯函数，使后续接线只需一行调用即可启用，而默认关闭时现有运行逐字节不变。

// preserveSectionsEnabled 报告摘要章节保留是否开启。reg 为 nil 时按默认关闭处理
// （fail-closed），因此零值注册表与未装配注册表的调用方都得不到任何行为变化。
func preserveSectionsEnabled(reg *OptInRegistry) bool {
	if reg == nil {
		return false
	}
	return reg.Enabled(OptInPreserveSummarySections)
}

// preserveSummarySections 在开关开启时把原摘要中的关键章节并入重建摘要；开关关闭时
// 原样返回 rebuilt，逐字节不变（包括空串、尾部空白、CJK 与任意字节序列）。
//
// 它是纯函数：只读入参，不修改 rebuilt / orig，不读取进程环境或其他全局状态；相同的
// 入参与开关状态总得到相同结果。关闭分支刻意不调用 session.PreserveSections，确保默认
// 路径没有任何依赖行为变化。开启分支直接委托给 session 包的领域实现。
func preserveSummarySections(rebuilt, orig string, reg *OptInRegistry) string {
	if !preserveSectionsEnabled(reg) {
		return rebuilt
	}
	return session.PreserveSections(rebuilt, orig)
}
