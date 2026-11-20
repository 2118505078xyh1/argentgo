package agent

import (
	"context"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// finalAnswerPrompt 在步骤预算耗尽后作为一条合成的用户轮次注入。
// 它不带任何工具规格，因此会促使模型总结已有的内容，而不是开启它已无力承担的又一轮工具调用。
const finalAnswerPrompt = "The maximum number of steps has been reached. Do not call any tools. Give your best final answer now using only the information you already have."

// finalAnswer 在轮次预算耗尽时，用最后一次请求换取一个无工具的答案。
// 它使用普通的 Chatter，因此保持本地可离线测试。提供方失败不是致命的：
// 运行仍以 StopMaxTurns 结束，只是没有新答案。取消则不同——它会被返回，
// 让循环报告 StopCancelled，而不是被吞进一条通知后返回成功结果。
func (s *runState) finalAnswer(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base := len(s.messages)
	s.messages = append(s.messages, llm.Message{Role: "user", Content: finalAnswerPrompt})

	savedSpecs := s.specs
	s.specs = nil // 无工具：回复无法开启又一轮工具调用
	turn := s.turns
	// 只发一次请求：没有回退链，因此预算耗尽后不会因重试而被拉长。
	// 增量先缓冲、仅在成功时刷出，与 chatWithFallback 一致：被取消或失败的请求
	// 绝不能把半截答案泄漏到操作人员的流中。
	var deltas []string
	reply, err := s.chat(ctx, func(delta string) {
		deltas = append(deltas, delta)
	})
	s.specs = savedSpecs
	if err != nil {
		s.messages = s.messages[:base]
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		s.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("could not produce a final answer: %v", err)})
		return nil
	}
	for _, delta := range deltas {
		s.emit(Event{Kind: EventTextDelta, Turn: turn, Text: delta})
	}
	if reply.Content == "" {
		// 空答案不值得在记录中留下一条合成的用户轮次。
		s.messages = s.messages[:base]
		return nil
	}

	// 保持统计真实：这是一次真实的模型请求，即使它不算作刚耗尽预算中的一轮。
	_, _ = s.account(reply, EstimateMessages(s.messages))

	// 这里只应放入正文；预算耗尽后无法回应误发的工具调用，
	// 因此刻意把它从记录中丢弃。
	s.messages = append(s.messages, llm.Message{
		Role:      "assistant",
		Content:   reply.Content,
		Reasoning: reply.Reasoning,
	})
	s.lastText = reply.Content
	s.record("user", finalAnswerPrompt)
	s.record("assistant", reply)
	return nil
}
