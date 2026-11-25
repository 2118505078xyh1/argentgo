package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/hooks"
	"example.com/argentgo/internal/llm"
)

// hookCtxDispatcher 返回预设的 Result，用于观察 hookBridge.fire 对附加上下文的入队行为。
type hookCtxDispatcher struct {
	result hooks.Result
	err    error
	calls  int
}

func (d *hookCtxDispatcher) Dispatch(context.Context, *hooks.Config, hooks.Payload) (hooks.Result, error) {
	d.calls++
	return d.result, d.err
}

// hookCtxCaptureChatter 捕获每次请求时的完整消息列表，返回无工具调用的纯文本答案。
type hookCtxCaptureChatter struct {
	messages []llm.Message
}

func (c *hookCtxCaptureChatter) Chat(_ context.Context, _ string, messages []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	c.messages = append([]llm.Message(nil), messages...)
	return llm.Message{Role: "assistant", Content: "ok"}, nil
}

// hookCtxEmptyTools 是空工具注册表：循环不会进入工具批次。
type hookCtxEmptyTools struct{}

func (hookCtxEmptyTools) Specs() []llm.ToolSpec { return nil }
func (hookCtxEmptyTools) Run(context.Context, string, string) (string, error) {
	return "", errors.New("no tools available")
}

// injectedUserTurns 通过一次真实的 agent 运行把队列内容变成模型可见的用户轮次。
// 这样 app 侧无需访问 agent 包的未导出 drain()，即可断言"hook 上下文确实被注入"。
func injectedUserTurns(t *testing.T, q *agent.HookContextQueue) []string {
	t.Helper()
	chat := &hookCtxCaptureChatter{}
	runner := &agent.Runner{Opts: agent.Options{Model: "m", Chatter: chat, Tools: hookCtxEmptyTools{}}}
	if _, err := runner.Run(agent.WithHookContextQueue(context.Background(), q), "prompt"); err != nil {
		t.Fatalf("agent run: %v", err)
	}
	var users []string
	for _, m := range chat.messages {
		if m.Role == "user" && m.Content != "prompt" {
			users = append(users, m.Content)
		}
	}
	return users
}

// TestHookBridgeEnqueuesAndAgentInjects 覆盖 fire → 队列 → agent 注入的链路：
// 钩子返回的附加上下文最终作为用户轮次出现在模型请求中，CJK 原样保留。
func TestHookBridgeEnqueuesAndAgentInjects(t *testing.T) {
	q := &agent.HookContextQueue{}
	bridge := &hookBridge{
		dispatcher: &hookCtxDispatcher{result: hooks.Result{
			AdditionalContexts: []string{"注意：只改 loop.go", "记得跑 go vet"},
		}},
		config:   &hooks.Config{},
		hookCtxQ: q,
	}
	result, err := bridge.fire(context.Background(), hooks.PostToolUse, hooks.Payload{})
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if !reflect.DeepEqual(result.AdditionalContexts, []string{"注意：只改 loop.go", "记得跑 go vet"}) {
		t.Fatalf("result = %+v", result)
	}
	got := injectedUserTurns(t, q)
	want := []string{"注意：只改 loop.go", "记得跑 go vet"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("injected user turns = %q, want %q", got, want)
	}
}

// TestHookBridgeNoQueueOrNoContextsByteIdentical 确认未接线队列、无附加上下文、分发失败三种情况
// 都不入队也不改变 fire 的返回（默认运行逐字节不变）。
func TestHookBridgeNoQueueOrNoContextsByteIdentical(t *testing.T) {
	base := hooks.Result{Blocked: true, Message: "被拦截"}
	t.Run("nil queue", func(t *testing.T) {
		want := hooks.Result{Blocked: true, Message: "被拦截", AdditionalContexts: []string{"不应被入队"}}
		bridge := &hookBridge{
			dispatcher: &hookCtxDispatcher{result: want},
			config:     &hooks.Config{},
		}
		result, err := bridge.fire(context.Background(), hooks.PreToolUse, hooks.Payload{})
		if err != nil || !reflect.DeepEqual(result, want) {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
	})
	t.Run("no contexts", func(t *testing.T) {
		q := &agent.HookContextQueue{}
		bridge := &hookBridge{dispatcher: &hookCtxDispatcher{result: base}, config: &hooks.Config{}, hookCtxQ: q}
		result, err := bridge.fire(context.Background(), hooks.PreToolUse, hooks.Payload{})
		if err != nil || !reflect.DeepEqual(result, base) {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		if got := injectedUserTurns(t, q); len(got) != 0 {
			t.Fatalf("unexpected injected turns: %q", got)
		}
	})
	t.Run("dispatch error", func(t *testing.T) {
		wantErr := errors.New("boom")
		wantResult := hooks.Result{AdditionalContexts: []string{"x"}}
		q := &agent.HookContextQueue{}
		bridge := &hookBridge{
			dispatcher: &hookCtxDispatcher{result: wantResult, err: wantErr},
			config:     &hooks.Config{}, hookCtxQ: q,
		}
		result, err := bridge.fire(context.Background(), hooks.PreToolUse, hooks.Payload{})
		if !errors.Is(err, wantErr) || !reflect.DeepEqual(result, wantResult) {
			t.Fatalf("result = %+v, err = %v", result, err)
		}
		// 分发失败时不入队。
		if got := injectedUserTurns(t, q); len(got) != 0 {
			t.Fatalf("unexpected injected turns: %q", got)
		}
	})
}

// TestHookBridgeMultipleFiresAccumulate 确认多次 fire 的上下文累积到同一队列，
// 跨 fire 去重且不丢失不同文本（模拟多个工具调用/生命周期事件）。
func TestHookBridgeMultipleFiresAccumulate(t *testing.T) {
	q := &agent.HookContextQueue{}
	d := &hookCtxDispatcher{}
	bridge := &hookBridge{dispatcher: d, config: &hooks.Config{}, hookCtxQ: q}
	for _, texts := range [][]string{
		{"第一条", "第二条"},
		{"第二条", "第三条"},
		{"第四条"},
	} {
		d.result = hooks.Result{AdditionalContexts: texts}
		if _, err := bridge.fire(context.Background(), hooks.PostToolUse, hooks.Payload{}); err != nil {
			t.Fatalf("fire: %v", err)
		}
	}
	if d.calls != 3 {
		t.Fatalf("dispatch calls = %d, want 3", d.calls)
	}
	got := injectedUserTurns(t, q)
	want := []string{"第一条", "第二条", "第三条", "第四条"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("injected user turns = %q, want %q", got, want)
	}
}

// TestHookAdditionalContextEndToEnd 串联 hooks 与 app 两侧：真实 Dispatcher 的注入式 runner
// 在 stdout 中声明 additionalContext，经 Outcome 提取、Summarize 聚合、fire 入队，
// 最终由 agent 在轮次边界作为用户轮次注入模型上下文。
func TestHookAdditionalContextEndToEnd(t *testing.T) {
	q := &agent.HookContextQueue{}
	dispatcher := &hooks.Dispatcher{Runner: func(_ context.Context, command hooks.Command, _ hooks.Payload) hooks.Outcome {
		return hooks.Outcome{
			Event: command.Event, Command: command.Command, ExitCode: 0,
			Stdout: `{"additionalContext":"请只修改 docs 目录下的文件"}`,
		}
	}}
	bridge := &hookBridge{
		dispatcher: dispatcher,
		config:     &hooks.Config{Commands: []hooks.Command{{Event: hooks.PostToolUse, Command: "ctx-hook"}}},
		hookCtxQ:   q,
	}
	result, err := bridge.fire(context.Background(), hooks.PostToolUse, hooks.Payload{})
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if !reflect.DeepEqual(result.AdditionalContexts, []string{"请只修改 docs 目录下的文件"}) {
		t.Fatalf("result.AdditionalContexts = %q", result.AdditionalContexts)
	}
	got := injectedUserTurns(t, q)
	if !reflect.DeepEqual(got, []string{"请只修改 docs 目录下的文件"}) {
		t.Fatalf("injected user turns = %q", got)
	}
}
