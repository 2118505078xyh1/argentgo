package server

import (
	"context"
	"errors"
)

// TurnEndReason 描述 agent 一轮是如何结束的。ACP session/prompt
// 结果历史上只暴露一个 stopReason，足以区分
// "cancelled" 回复与 "end_turn" 回复，但无法指出底层
// 原因。TurnEndReason 就是这种三分类：用户/传输
// 中断、真正的失败，或干净的完成。
//
// 该值作为附加的 omitempty 字段携带在终态结果上，
// 因此只理解 stopReason 的客户端看到的仍是原始的线协议
// 形态和行为。
type TurnEndReason string

const (
	// TurnEndInterrupted 表示用户或传输停止了本轮
	// （可能被包装的 context.Canceled 或显式取消）。
	TurnEndInterrupted TurnEndReason = "interrupted"
	// TurnEndFailure 表示本轮以真正的、非取消的错误结束。
	TurnEndFailure TurnEndReason = "failure"
	// TurnEndComplete 表示本轮无错误地完成。
	TurnEndComplete TurnEndReason = "complete"
)

// ClassifyTurnEnd 把 Runner.Prompt 调用的结果映射到
// TurnEndReason。取消——无论是调用方的显式信号，还是
// 一个（可能被包装的）context.Canceled 错误——都是中断；
// 其他错误都是失败；无取消的 nil 错误是干净的完成。
//
// 截止时间有意*不*算取消（与 isCancelled 一致）：
// context.DeadlineExceeded 无论是否被包装，都算失败。
// 仅包含 "context canceled" 字样但不是该哨兵的错误
// 也仍是失败，因此失败永不会被误报为用户中断。
func ClassifyTurnEnd(err error, canceled bool) TurnEndReason {
	if canceled || errors.Is(err, context.Canceled) {
		return TurnEndInterrupted
	}
	if err != nil {
		return TurnEndFailure
	}
	return TurnEndComplete
}

// turnEndErrorData 是 session/prompt 失败路径上携带的附加 JSON-RPC error.data
// 载荷。JSON-RPC 2.0 允许错误对象带有可选的 data 成员，因此只检查
// code/message 的客户端看到的仍是完全原始的错误信封，而消费 turnEndReason 的
// 客户端终于可以读到两种结果形态路径已经携带的
// "failure" 分类。
type turnEndErrorData struct {
	TurnEndReason TurnEndReason `json:"turnEndReason"`
}

// failureTurnEndData 返回真正的一轮失败的 error.data。
// 它是纯辅助函数，因此无需启动传输
// 即可断言失败分类。
func failureTurnEndData(reason TurnEndReason) turnEndErrorData {
	return turnEndErrorData{TurnEndReason: reason}
}
