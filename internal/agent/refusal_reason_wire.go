package agent

import (
	"strings"
	"sync/atomic"

	"example.com/argentgo/internal/security"
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

// AppendRefusalModelHint 是 dispatch.go 错误分支的一次性接线包装：仅当 opt-in 开启、
// 且 refusal 命中了已知拒绝类别时，才把模型可读提示追加到 errText 末尾，使拒绝决策
// 对模型可见（此前 refusal 只在循环内部被分类，模型拿到的仍是裸错误）。
//
// 关闭（默认）或未命中任何已知类别（零值 refusal）时逐字节原样返回 errText，
// 既不注入零值 RefusalReason 的兜底提示，也不触碰事件/会话记录的原始字节。
// 追加采用换行分隔并先修剪 errText 尾部空白，与 augmentNoopEditOutput 的提示追加方式一致；
// 提示本身由 Kind/Recoverable 决定（稳定中文），按 rune 拼接不会切坏 CJK 字节。
// 幂等：若 errText 已含相同提示则不再追加，多次拒绝不会重复堆叠同一段指引。
func AppendRefusalModelHint(errText string, refusal security.RefusalReason) string {
	if !refusalReasonEnabled() || refusal == (security.RefusalReason{}) {
		return errText
	}
	hint := refusal.ModelHint()
	if hint == "" || strings.Contains(errText, hint) {
		return errText
	}
	trimmed := strings.TrimRight(errText, " \t\r\n")
	if trimmed == "" {
		return hint
	}
	return trimmed + "\n" + hint
}
