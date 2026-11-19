package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argent-go/internal/llm"
)

// taskArgsJSON builds delegate arguments.
func taskArgsJSON(description, prompt string) string {
	return `{"description":"` + description + `","prompt":"` + prompt + `"}`
}

func TestSubagentReturnsChildText(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}}
	childEvents := &eventSink{}
	parentEvents := &eventSink{}
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			return textReply("child says 42"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root got: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("look", "find the answer"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		Emit:     parentEvents.emit,
		Subagent: &SubagentConfig{OnEvent: childEvents.emit},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Text, "child says 42") {
		t.Fatalf("text = %q, want the child's answer", res.Text)
	}
	if !parentEvents.has(EventSubagentStart) || !parentEvents.has(EventSubagentEnd) {
		t.Fatal("parent did not see subagent start/end events")
	}
	sawChildDepth := false
	for _, event := range childEvents.events {
		if event.Depth == 1 && event.Kind == EventTurnStart {
			sawChildDepth = true
		}
	}
	if !sawChildDepth {
		t.Fatal("child events did not carry depth 1")
	}
}

func TestSubagentRestrictsTools(t *testing.T) {
	tools := &fakeTools{specs: []llm.ToolSpec{toolSpec("read"), toolSpec("write")}}
	var childToolNames []string
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, specs []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			if len(childToolNames) == 0 {
				for _, spec := range specs {
					childToolNames = append(childToolNames, spec.Function.Name)
				}
			}
			if strings.HasPrefix(lastToolContent(messages), "error:") {
				return textReply("child: " + lastToolContent(messages)), nil
			}
			return toolReply(toolCall("c1", "write", `{}`)), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("edit", "change the file"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    tools,
		Subagent: &SubagentConfig{AllowedTools: []string{"read"}},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(childToolNames) == 0 {
		t.Fatalf("child tools = %v, want read plus the delegate tool", childToolNames)
	}
	for _, name := range childToolNames {
		if name == "write" {
			t.Fatalf("child tools = %v, write must be filtered out", childToolNames)
		}
	}
	if !strings.Contains(strings.Join(childToolNames, ","), "read") {
		t.Fatalf("child tools = %v, want read", childToolNames)
	}
	if !strings.Contains(res.Text, "not available to subagents") {
		t.Fatalf("text = %q, want the restricted-tool error", res.Text)
	}
}

func TestSubagentDepthLimit(t *testing.T) {
	childRequests := 0
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			if lastUserContent(messages) != "level one" {
				t.Fatal("a depth-2 subagent ran")
			}
			childRequests++
			if childRequests == 1 {
				return toolReply(toolCall("c1", TaskToolName, taskArgsJSON("deeper", "level two"))), nil
			}
			return textReply("child: " + lastToolContent(messages)), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("first", "level one"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Subagent: &SubagentConfig{MaxDepth: 1},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Text, "depth limit") {
		t.Fatalf("text = %q, want the depth-limit error", res.Text)
	}
	if childRequests != 2 {
		// One request asks for the nested task, the second answers after the
		// depth-limit error result.
		t.Fatalf("child requests = %d, want 2 (the nested run must not start)", childRequests)
	}
}

func TestSubagentHasItsOwnTurnBudget(t *testing.T) {
	childRequests := 0
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			childRequests++
			return toolReply(toolCall("c1", "read", `{}`)), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root: " + last), nil
		}
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("loop", "keep reading"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Subagent: &SubagentConfig{Budget: Budget{MaxTurns: 2}},
	}}

	res, err := runner.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if childRequests != 2 {
		t.Fatalf("child requests = %d, want its own cap of 2", childRequests)
	}
	if !strings.Contains(res.Text, "without an answer") {
		t.Fatalf("text = %q, want the empty-answer placeholder", res.Text)
	}
}
