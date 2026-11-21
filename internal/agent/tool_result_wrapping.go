package agent

import "sync/atomic"

// toolResultWrapping 控制是否在回灌给模型的消息中把工具输出封装成带“不可信来源”
// 标记的边界块，默认关闭。这是进程级 opt-in：封装只作用于 appendTool 写入的消息
// 内容，不改变事件与会话记录中保存的原始输出字节。
var toolResultWrapping atomic.Bool

// SetToolResultWrapping 打开或关闭工具结果封装接线。
// 关闭（默认）时，appendTool 写入的消息内容逐字节保持原样。
func SetToolResultWrapping(enabled bool) {
	toolResultWrapping.Store(enabled)
}

// toolResultWrappingEnabled 报告当前是否启用了工具结果封装。
func toolResultWrappingEnabled() bool {
	return toolResultWrapping.Load()
}
