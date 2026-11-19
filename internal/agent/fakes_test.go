package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// fakeChatter scripts model replies without a network.
type fakeChatter struct {
	mu       sync.Mutex
	requests int
	models   []string
	seen     [][]llm.Message
	toolSets [][]llm.ToolSpec
	fn       func(n int, messages []llm.Message, tools []llm.ToolSpec, onText func(string)) (llm.Message, error)
}

func (f *fakeChatter) Chat(
	ctx context.Context,
	model string,
	messages []llm.Message,
	tools []llm.ToolSpec,
	onText func(string),
) (llm.Message, error) {
	f.mu.Lock()
	f.requests++
	n := f.requests
	f.models = append(f.models, model)
	f.seen = append(f.seen, append([]llm.Message(nil), messages...))
	f.toolSets = append(f.toolSets, append([]llm.ToolSpec(nil), tools...))
	f.mu.Unlock()
	return f.fn(n, messages, tools, onText)
}

func (f *fakeChatter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakeChatter) modelsSeen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

func (f *fakeChatter) requestsSeen() [][]llm.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]llm.Message, len(f.seen))
	copy(out, f.seen)
	return out
}

// fakeTools is a scripted ToolRunner that records every call.
type fakeTools struct {
	specs []llm.ToolSpec
	run   func(ctx context.Context, name, args string) (string, error)

	mu    sync.Mutex
	calls []string
}

func (f *fakeTools) Specs() []llm.ToolSpec { return f.specs }

func (f *fakeTools) Run(ctx context.Context, name, args string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	if f.run != nil {
		return f.run(ctx, name, args)
	}
	return name + " ok", nil
}

func (f *fakeTools) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTools) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

// toolSpec builds a minimal advertised tool.
func toolSpec(name string) llm.ToolSpec {
	var spec llm.ToolSpec
	spec.Type = "function"
	spec.Function.Name = name
	spec.Function.Description = "test tool " + name
	spec.Function.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
	return spec
}

// toolCall builds a model-requested call.
func toolCall(id, name, args string) llm.ToolCall {
	return llm.ToolCall{
		ID:   id,
		Type: "function",
		Function: llm.ToolCallFunction{
			Name:      name,
			Arguments: args,
		},
	}
}

// textReply is an assistant answer with no tool calls.
func textReply(text string) llm.Message {
	return llm.Message{Role: "assistant", Content: text}
}

// toolReply is an assistant turn that asks for tools.
func toolReply(calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: "assistant", ToolCalls: calls}
}

// lastToolContent returns the newest tool result in a conversation.
func lastToolContent(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" {
			return messages[i].Content
		}
	}
	return ""
}

// containsUser reports whether a user message with exactly text exists.
func containsUser(messages []llm.Message, text string) bool {
	for _, msg := range messages {
		if msg.Role == "user" && msg.Content == text {
			return true
		}
	}
	return false
}

// isSubagent reports whether a conversation belongs to a nested run.
func isSubagent(messages []llm.Message) bool {
	return len(messages) > 0 && strings.Contains(messages[0].Content, "You are a subagent")
}

// lastUserContent returns the newest user message.
func lastUserContent(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

// eventSink collects emitted events.
type eventSink struct {
	mu     sync.Mutex
	events []Event
}

func (s *eventSink) emit(event Event) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *eventSink) kinds() []EventKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]EventKind, len(s.events))
	for i, event := range s.events {
		out[i] = event.Kind
	}
	return out
}

func (s *eventSink) has(kind EventKind) bool {
	for _, event := range s.events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}
