package hooks

import (
	"context"
	"errors"
)

// turnEndEventKind 把一个 agent 回合的终止结果映射为应触发的钩子事件名。
//
// 它是纯函数：不读取外部状态、不访问时钟、不修改输入，给定相同参数总返回
// 相同结果，因此与 CJK 等本地化文本无关，可稳定地用于并发分发。
//
// 映射规则：
//   - canceled 为真，或 err 是（可能被包装或合并的）context.Canceled：interrupt；
//   - 其它任何非 nil 错误：stop_failure；
//   - 既无取消也无错误：turn_end。
//
// 截止时间（context.DeadlineExceeded）刻意不算取消，始终算失败，与服务端
// ClassifyTurnEnd 的划分一致。判定只用 errors.Is 识别哨兵错误，从不解析错误
// 文本，因此仅字面含 "context canceled" 的真实失败不会被误报为用户中断。
func turnEndEventKind(canceled bool, err error) string {
	if canceled || errors.Is(err, context.Canceled) {
		return string(EventInterrupt)
	}
	if err != nil {
		return string(EventStopFailure)
	}
	return string(EventTurnEnd)
}

// ClassifyTurnEndEvent 是 turnEndEventKind 的导出别名，供包外调用方直接使用；
// 返回值本身就是可交给 Dispatcher 的 Event 名。
func ClassifyTurnEndEvent(canceled bool, err error) string {
	return turnEndEventKind(canceled, err)
}
