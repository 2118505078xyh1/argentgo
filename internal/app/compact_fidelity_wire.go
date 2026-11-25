package app

import (
	"strings"

	"example.com/argentgo/internal/session"
)

// 本文件提供"压缩摘要保真度告警"的 app 侧装配骨架。它刻意保持零调用方：不读取进程
// 环境、不注册回调、不触碰压缩完成点。唯一职责是提供一个纯函数与一个可注入的观察者
// 工厂，使后续接线只需一行调用即可启用，而默认关闭时现有运行逐字节不变。

// compactionFidelityNoticePrefix 是保真度告警的固定前缀，便于 UI 与测试稳定识别，
// 也不会与压缩流程自身的通知混淆。
const compactionFidelityNoticePrefix = "[compaction fidelity] "

// compactionFidelityMinScore 传给 session.IsCompactionLossy 的覆盖度阈值。
// 取 1 表示质量告警要求四要素（目标、改动文件、失败、下一步）全部齐全：只要缺任意
// 一项，Score 就低于 1 而被判为有损。这样"仅缺下一步"也能触发告警，与告警预期一致。
const compactionFidelityMinScore = 1.0

// compactionFidelityOn 报告压缩保真度告警是否开启。reg 为 nil 时按默认关闭处理
// （fail-closed），因此零值注册表与未装配注册表的调用方都得不到任何行为变化。
func compactionFidelityOn(reg *OptInRegistry) bool {
	if reg == nil {
		return false
	}
	return reg.Enabled(OptInCompactionFidelity)
}

// compactFidelityNotice 测量一条压缩摘要的要素覆盖度，并在判为有损时返回一条稳定的
// 中文告警文本；否则返回空串。
//
// 它是纯函数：只读入参、不修改 summary，也不读取任何全局状态。告警文本按固定顺序
// 列出缺失的要素（目标、改动文件、失败与错误、下一步），因此相同的 summary 总得到
// 相同的文本。阈值见 compactionFidelityMinScore。
func compactFidelityNotice(summary string) string {
	if !session.IsCompactionLossy(summary, compactionFidelityMinScore) {
		return ""
	}

	fidelity := session.MeasureCompactionFidelity(summary)
	// 按固定顺序列出缺失要素，保证告警文本稳定且与章节顺序一致。
	var missing []string
	if !fidelity.HasGoal {
		missing = append(missing, "目标")
	}
	if fidelity.ChangedFiles == 0 {
		missing = append(missing, "改动文件")
	}
	if fidelity.FailureCount == 0 {
		missing = append(missing, "失败与错误")
	}
	if fidelity.NextSteps == 0 {
		missing = append(missing, "下一步")
	}
	if len(missing) == 0 {
		// 保险分支：判为有损却没有明细时仍给出确定性文案，不返回空串造成告警丢失。
		return compactionFidelityNoticePrefix + "摘要要素覆盖度不足"
	}
	return compactionFidelityNoticePrefix + "摘要缺少" + strings.Join(missing, "、")
}

// newCompactionFidelityObserver 构造一个可注入到压缩完成点的观察者闭包。
//
// 关闭（reg 为 nil、开关未开启）时返回 nil，调用方据此不装配观察者，主循环行为不变。
// 开启时返回的闭包即 compactFidelityNotice：接收摘要文本，有损时返回告警文本，否则空串。
// 闭包只读取入参，不捕获可变状态，可安全并发调用。
func newCompactionFidelityObserver(reg *OptInRegistry) func(summary string) string {
	if !compactionFidelityOn(reg) {
		return nil
	}
	return compactFidelityNotice
}
