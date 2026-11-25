package agent

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/llm"
)

// taskArgsJSON 构建委派参数。
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

// 子循环继承父级注入的持久性钩子，与旧版进程级注册表的行为一致（子运行也会刷写根会话）。
// （接上行）
// 该标志在父级发出 task 调用时翻转一次，在子级的 chatter 运行时再翻转一次，因此只有在这两点之间触发的钩子才算作
// 子级自己的屏障。
// （接上行）
func TestSubagentInheritsBeforeRequestHook(t *testing.T) {
	t.Parallel()

	childPending, childHooks := false, 0
	chat := &fakeChatter{fn: func(_ int, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
		if isSubagent(messages) {
			childPending = false // 子级自己的钩子在此之前已经运行过
			return textReply("child says 42"), nil
		}
		if last := lastToolContent(messages); last != "" {
			return textReply("root got: " + last), nil
		}
		childPending = true // 即将派发派生子级的 task 工具
		return toolReply(toolCall("t1", TaskToolName, taskArgsJSON("look", "find the answer"))), nil
	}}
	runner := &Runner{Opts: Options{
		Model:    "m",
		Chatter:  chat,
		Tools:    &fakeTools{specs: []llm.ToolSpec{toolSpec("read")}},
		Subagent: &SubagentConfig{},
		BeforeRequest: func() error {
			if childPending {
				childHooks++
			}
			return nil
		},
	}}

	if _, err := runner.Run(context.Background(), "delegate"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if childHooks != 1 {
		t.Fatalf("child before-request hook calls = %d, want 1", childHooks)
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
		// 一次请求嵌套任务，第二次在深度限制错误结果之后回答。
		// （接上行）
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
	if childRequests != 3 {
		// 两个计入预算的轮次加上唯一的无工具最终答案请求。
		t.Fatalf("child requests = %d, want 2 turns plus one final answer", childRequests)
	}
	if !strings.Contains(res.Text, "without an answer") {
		t.Fatalf("text = %q, want the empty-answer placeholder", res.Text)
	}
}
