package agent

import "sync"

// omitContextFunc 是 agent 注入给 app 层的窄回调：可重试失败后、下一次请求之前，
// 把失败尝试的助手消息及其工具结果以 session.AppendContextEdit{TargetID, Text:""}
// 永久从「模型可见」上下文省略（不改动磁盘历史，上下文编辑是投影期的动作）。
// turn 是失败尝试所属的轮次，由 app 层据此定位要省略的记录 ID。
//
// agent 包不依赖 session，因此省略动作必须由 app 层实现并经由此回调注入。
// 生产默认值为 nil（no-op），因此未接线的运行与接线前逐字节一致。
type omitContextFunc func(turn int) error

var (
	retryOmitHookMu sync.RWMutex
	retryOmitHook   omitContextFunc
)

// SetRetryOmitHook 安装省略回调；nil 恢复为 no-op 默认。
//
// 门控关闭时该钩子从不被读取，因此全局状态对默认路径无影响。
// app 层在 opt-in 开启时注入按会话的回调，把失败尝试的消息以 ContextEdit 省略。
func SetRetryOmitHook(fn func(turn int) error) {
	retryOmitHookMu.Lock()
	if fn == nil {
		retryOmitHook = nil
	} else {
		retryOmitHook = omitContextFunc(fn)
	}
	retryOmitHookMu.Unlock()
}

// currentRetryOmitHook 返回当前安装的省略回调，未安装时返回 nil。
func currentRetryOmitHook() omitContextFunc {
	retryOmitHookMu.RLock()
	defer retryOmitHookMu.RUnlock()
	return retryOmitHook
}
