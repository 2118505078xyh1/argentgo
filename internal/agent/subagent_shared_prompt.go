package agent

import "sync/atomic"

// 子代理共享父级 system prompt 的开关。迁移自 killi 决策 20：
// 子代理首请求复用父级已缓存的前缀，可显著降低 token 成本。
//
// 默认开启，由装配层按 ARGENT_SUBAGENT_SHARED_PROMPT 统一设置；仅 =0 时紧急关闭，
// 此时子代理沿用原有 subagentTemplate，行为与历史逐字节一致。
var subagentSharedPrompt atomic.Bool

// SetSubagentSharedPrompt 打开或关闭"子代理复用父级 system prompt"。
// 由装配层按 ARGENT_SUBAGENT_SHARED_PROMPT 统一设置，=0 时关闭。
func SetSubagentSharedPrompt(enabled bool) {
	subagentSharedPrompt.Store(enabled)
}

// subagentSharedPromptEnabled 报告当前是否启用共享 prompt。
func subagentSharedPromptEnabled() bool {
	return subagentSharedPrompt.Load()
}
