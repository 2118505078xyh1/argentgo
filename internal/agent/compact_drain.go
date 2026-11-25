package agent

import (
	"sync/atomic"

	"example.com/argentgo/internal/llm"
)

// 本文件实现项目 TODO #126（学自 codex）：压缩后仍超预算时，按缓存前缀
// 逐条淘汰最旧的非系统消息，而不是直接放弃重放。默认开启、ARGENT_COMPACT_DRAIN=0 关闭。

// maxCompactDrain 是单次溢出的最多淘汰条数，防止在病态输入下无限循环。
const maxCompactDrain = 50

var compactDrain atomic.Bool

// SetCompactDrain 打开或关闭"压缩后逐条淘汰"恢复路径。
// 由装配层按 ARGENT_COMPACT_DRAIN 统一设置；=0 关闭时 compactAndReplay 行为逐字节不变。
func SetCompactDrain(enabled bool) {
	compactDrain.Store(enabled)
}

// compactDrainEnabled 报告当前是否启用逐条淘汰。
func compactDrainEnabled() bool {
	return compactDrain.Load()
}

// drainOldestForBudget 从 messages 中逐条移除最旧的非系统消息，
// 直到估算不超过 window 或达到 maxRemove 条。始终保留 messages[0]（system）
// 和最后一条消息（通常是用户提示），并以新切片返回，绝不修改入参底层数组。
func drainOldestForBudget(messages []llm.Message, window, maxRemove int) ([]llm.Message, int) {
	if len(messages) <= 2 || maxRemove <= 0 {
		return messages, 0
	}
	out := append([]llm.Message(nil), messages...)
	removed := 0
	for EstimateMessages(out) > window && removed < maxRemove && len(out) > 2 {
		out = append(out[:1:1], out[2:]...) // 保留 system，丢弃最旧的消息
		removed++
	}
	return out, removed
}
