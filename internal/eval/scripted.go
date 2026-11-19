package eval

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// Scripted is an llm.Chatter that replays a scenario's script instead of
// talking to a provider. It records every request so the harness can check
// expect_request_contains and count turns.
type Scripted struct {
	mu     sync.Mutex
	turns  []ScriptTurn
	repeat bool
	calls  int
	// history holds the messages of every request, in call order.
	history [][]llm.Message
	// violations collects broken expect_request_contains expectations.
	violations []string
}

var _ llm.Chatter = (*Scripted)(nil)

// NewScripted builds a provider that plays turns in order. With repeat, the
// script loops forever, so the scripted model never stops on its own.
func NewScripted(turns []ScriptTurn, repeat bool) *Scripted {
	return &Scripted{turns: append([]ScriptTurn(nil), turns...), repeat: repeat}
}

// Chat returns the next scripted turn.
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
		// A real gateway always answers; an exhausted script says so instead
		// of hanging the loop.
		reply := "the script ended"
		emit(onText, reply)
		return llm.Message{Role: "assistant", Content: reply}, nil
	}

	reply := llm.Message{Role: "assistant", Content: turn.Respond.Text, Usage: turn.Respond.Usage}
	if reply.Content != "" {
		emit(onText, reply.Content)
	}
	for i, call := range turn.Respond.ToolCalls {
		// The id must be unique per request: an identical id on two calls in
		// the same history confuses tool-result pairing.
		reply.ToolCalls = append(reply.ToolCalls, llm.ToolCall{
			ID:   fmt.Sprintf("call-%d-%d", index+1, i+1),
			Type: "function",
			Function: llm.ToolCallFunction{
				Name:      call.Name,
				Arguments: call.ArgumentsString(),
			},
		})
	}
	return reply, nil
}

// pick returns the turn for a call index.
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

// Calls is the number of model turns served so far.
func (s *Scripted) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Violations lists the broken expect_request_contains expectations.
func (s *Scripted) Violations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.violations...)
}

// History returns the messages of every request, in call order.
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

// flatten renders the request as searchable text: message contents, tool call
// names and arguments, tool results.
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
