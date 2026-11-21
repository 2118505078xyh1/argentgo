package agent

import (
	"sync/atomic"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// refusalReason 控制是否把工具错误分类成结构化拒绝原因写入 callResult，默认关闭。
// 这是进程级 opt-in：分类只填充内部字段，不改变错误文本、事件与会话记录的字节，
// 因此接线本身对既有运行完全透明。关闭时连 ReasonFromError 都不会被调用。
var refusalReason atomic.Bool

// SetRefusalReason 打开或关闭拒绝原因分类接线。
// 关闭（默认）时，runTool 写入的 callResult.refusal 始终为零值。
func SetRefusalReason(enabled bool) {
	refusalReason.Store(enabled)
}

// refusalReasonEnabled 报告当前是否启用了拒绝原因分类。
func refusalReasonEnabled() bool {
	return refusalReason.Load()
}

// classifyRefusal 仅在 opt-in 开启时把 err 映射成结构化拒绝原因。
// 关闭或未匹配任何已知错误时返回零值；它不读取也不改写输出文本。
func classifyRefusal(err error) security.RefusalReason {
	if !refusalReasonEnabled() {
		return security.RefusalReason{}
	}
	if reason, ok := security.ReasonFromError(err); ok {
		return reason
	}
	return security.RefusalReason{}
}
