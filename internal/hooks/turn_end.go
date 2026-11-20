package hooks

import (
	"context"
	"errors"
)

// ClassifyTurnEndEvent 将一个 agent 回合的结果映射到应结束它的生命周期
// 事件。它镜像 server.ClassifyTurnEnd 的三路划分，但返回钩子事件名，使调用方能直接发出结果
// 三路划分，但返回钩子事件名，使调用方能直接发出结果
// directly:
//
//	event := hooks.Event(hooks.ClassifyTurnEndEvent(canceled, err))
//
// 取消 —— 显式的 `canceled` 信号或（可能包装或合并的）context.Canceled 错误 —— 是中断。任何其他错误是
// 合并的）context.Canceled 错误 —— 是中断。任何其他错误是
// 失败，而无取消的 nil 错误是干净完成。
//
// 截止时间刻意不算取消（与服务端分类器一致）：
// 截止时间刻意不算取消（与服务端分类器一致）：context.DeadlineExceeded 无论是否包装都是失败。仅
// 字面包含 "context canceled" 而并非该哨兵的错误也算失败，因此真实失败不会被误报为
// 该哨兵的错误也算失败，因此真实失败不会被误报为
// 用户中断。
func ClassifyTurnEndEvent(canceled bool, err error) string {
	if canceled || errors.Is(err, context.Canceled) {
		return string(EventInterrupt)
	}
	if err != nil {
		return string(EventStopFailure)
	}
	return string(EventTurnEnd)
}
