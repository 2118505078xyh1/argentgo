package eval

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"example.com/argentgo/internal/llm"
)

// Scripted 是重放场景脚本而非与 provider 对话的 llm.Chatter。它记录每个请求，使 harness 能检查
// 与 provider 对话。它记录每个请求，使 harness 能检查
// expect_request_contains 并统计轮数。
type Scripted struct {
	mu     sync.Mutex
	turns  []ScriptTurn
	repeat bool
	calls  int
	// history 按调用顺序保存每个请求的消息。
	history [][]llm.Message
	// violations 收集被破坏的 expect_request_contains 期望。
	violations []string
}

var _ llm.Chatter = (*Scripted)(nil)

// NewScripted 构建一个按顺序播放各轮的 provider。使用 repeat 时脚本无限循环，因此脚本化模型永不自行停止。
// 脚本无限循环，因此脚本化模型永不自行停止。
func NewScripted(turns []ScriptTurn, repeat bool) *Scripted {
	return &Scripted{turns: append([]ScriptTurn(nil), turns...), repeat: repeat}
}

// Chat 返回下一条脚本化回复。
func (s *Scripted) Chat(ctx context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	if err := ctx.Err(); err != nil {
		return llm.Message{}, err
	}

	s.mu.Lock()
	index := s.calls
	s.calls++
	request := strings.Join(flatten(messages), "\n")
	s.history = append(s.history, append([]llm.Message(nil), messages...))
	turn, ok := s.pick(index)
	if ok {
		for _, needle := range turn.ExpectRequestContains {
			if !strings.Contains(request, needle) {
				s.violations = append(s.violations,
					fmt.Sprintf("turn %d: the request does not contain %q", index+1, needle))
			}
		}
		marked := false
		for _, message := range messages {
			if message.CacheControl != nil {
				marked = true
				break
			}
		}
		if turn.ExpectRequestCacheFree && marked {
			s.violations = append(s.violations,
				fmt.Sprintf("turn %d: the request carries a cache marker", index+1))
		}
		if turn.ExpectRequestCached && !marked {
			s.violations = append(s.violations,
				fmt.Sprintf("turn %d: the request has no cache marker", index+1))
		}
	}
	s.mu.Unlock()

	if !ok {
		// 真实网关总会回答；脚本耗尽时明确说明，而不是
		// 真实网关总会回答；脚本耗尽时明确说明，而不是挂住循环。
		reply := "the script ended"
		emit(onText, reply)
		return llm.Message{Role: "assistant", Content: reply}, nil
	}

	reply := llm.Message{Role: "assistant", Content: turn.Respond.Text, Usage: turn.Respond.Usage}
	if reply.Content != "" {
		emit(onText, reply.Content)
	}
	for i, call := range turn.Respond.ToolCalls {
		// id 在每次请求中必须唯一：同一历史中两次调用使用相同 id 会混淆工具结果配对。
		// 同一历史中两次调用使用相同 id 会混淆工具结果配对。
		reply.ToolCalls = append(reply.ToolCalls, llm.ToolCall{
			ID:   fmt.Sprintf("call-%d-%d", index+1, i+1),
			Type: "function",
			Function: llm.ToolCallFunction{
				// Validate 裁剪名称以检查其非空；分发时使用相同的规范值。手写脚本中带填充的名称必须到达作者想要的工具，而不是
				// 使用相同的规范值。手写脚本中带填充的名称
				// 必须到达作者想要的工具，而不是失败于
				// 在 registry 的精确名称查找中作为未知工具失败。
				Name:      strings.TrimSpace(call.Name),
				Arguments: call.ArgumentsString(),
			},
		})
	}
	return reply, nil
}

// pick 返回某个调用索引对应的轮次。
func (s *Scripted) pick(index int) (ScriptTurn, bool) {
	if len(s.turns) == 0 {
		return ScriptTurn{}, false
	}
	if s.repeat {
		return s.turns[index%len(s.turns)], true
	}
	if index < len(s.turns) {
		return s.turns[index], true
	}
	return ScriptTurn{}, false
}

// Calls 是迄今服务的模型轮数。
func (s *Scripted) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Violations 列出被破坏的 expect_request_contains 期望。
func (s *Scripted) Violations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.violations...)
}

// History 按调用顺序返回每个请求的消息。
func (s *Scripted) History() [][]llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]llm.Message, len(s.history))
	for i, messages := range s.history {
		out[i] = append([]llm.Message(nil), messages...)
	}
	return out
}

func emit(onText func(string), text string) {
	if onText != nil {
		onText(text)
	}
}

// flatten 将请求渲染为可搜索文本：消息内容、工具调用名与参数、工具结果。
// 名与参数、工具结果。
func flatten(messages []llm.Message) []string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		if message.Content != "" {
			parts = append(parts, message.Content)
		}
		for _, call := range message.ToolCalls {
			parts = append(parts, call.Function.Name+" "+call.Function.Arguments)
		}
	}
	return parts
}
