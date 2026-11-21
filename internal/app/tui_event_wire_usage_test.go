package app

import (
	"context"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestAgentToTUIEventUsageMapsFields 证明 usage 事件的三个 token 计数从当轮增量 Usage 复制到
// TUI 对应字段，且携带 tui.TuiEventUsage 种类。TotalUsage 刻意填入不同值，以确保映射取的是
// ev.Usage 而非运行总量——account() 不累加 CachedTokens，TotalUsage.Cached 恒为零。
func TestAgentToTUIEventUsageMapsFields(t *testing.T) {
	in := agent.Event{
		Kind:  agent.EventUsage,
		Usage: llm.Usage{PromptTokens: 120, CompletionTokens: 34, CachedTokens: 90, CacheWriteTokens: 7},
		// 运行总量与当轮不同；若误取它，下面三个字段必然不匹配。
		TotalUsage: llm.Usage{PromptTokens: 999, CompletionTokens: 888, CachedTokens: 0},
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("usage must have a TUI mapping")
	}
	want := tui.Event{
		Kind:             tui.TuiEventUsage,
		PromptTokens:     120,
		CompletionTokens: 34,
		CachedTokens:     90,
	}
	if got != want {
		t.Fatalf("mapped usage = %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventUsageZeroValueForwarded 证明零值用量也照常映射为带种类的 TUI 事件，
// 而不是被桥接层丢弃；是否隐藏空用量由更上层的门控决定，桥接层保持行为单一。
func TestAgentToTUIEventUsageZeroValueForwarded(t *testing.T) {
	got, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventUsage})
	if !ok {
		t.Fatal("zero-value usage must still have a TUI mapping")
	}
	want := tui.Event{Kind: tui.TuiEventUsage}
	if got != want {
		t.Fatalf("mapped zero usage = %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventUsageDeterministicNoTextLeak 证明映射是确定性的，且 agent 事件上的
// 自由格式文本（含 CJK）不会泄漏进 TUI 用量事件：TUI 用量事件只有 token 计数字段。
func TestAgentToTUIEventUsageDeterministicNoTextLeak(t *testing.T) {
	in := agent.Event{
		Kind:       agent.EventUsage,
		Text:       "中文思考过程 secret=sk-ant-abcdefghijklmnopqrst",
		ToolName:   "工具名",
		Usage:      llm.Usage{PromptTokens: 8, CompletionTokens: 3, CachedTokens: 2},
		TotalUsage: llm.Usage{PromptTokens: 16, CompletionTokens: 6, CachedTokens: 0},
	}
	first, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("usage must have a TUI mapping")
	}
	second, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("usage must have a TUI mapping on repeat")
	}
	if first != second {
		t.Fatalf("mapping is not deterministic: %+v vs %+v", first, second)
	}
	if first.Detail != "" || first.ToolName != "" || first.Args != "" || first.Output != "" {
		t.Fatalf("usage event leaked free-form text: %+v", first)
	}
	if first.Kind != tui.TuiEventUsage || first.PromptTokens != 8 || first.CompletionTokens != 3 || first.CachedTokens != 2 {
		t.Fatalf("usage event carried wrong counts: %+v", first)
	}
}

// TestAgentToTUIEventUsageDoesNotRegressOtherKinds 证明分派仍以 e.Kind 为键：其它事件上
// 附带的 Usage 负载既不会改变其映射，也不会把它们变成用量事件。
func TestAgentToTUIEventUsageDoesNotRegressOtherKinds(t *testing.T) {
	stray := llm.Usage{PromptTokens: 50, CompletionTokens: 60, CachedTokens: 70}

	tool, ok := AgentToTUIEvent(agent.Event{
		Kind: agent.EventToolStart, ToolCallID: "call-1", ToolName: "bash", Args: "ls", Usage: stray,
	})
	if !ok || tool.Kind != tui.EventToolStart || tool.PromptTokens != 0 {
		t.Fatalf("tool_start with a stray Usage payload regressed: %+v, ok=%v", tool, ok)
	}

	errEvt, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventError, Err: "stream reset", Usage: stray})
	if !ok || errEvt.Kind != tui.EventError || errEvt.Detail != "stream reset" || errEvt.PromptTokens != 0 {
		t.Fatalf("error with a stray Usage payload regressed: %+v, ok=%v", errEvt, ok)
	}

	usage, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventUsage, Usage: stray})
	if !ok || usage.Kind != tui.TuiEventUsage || usage.CachedTokens != 70 {
		t.Fatalf("usage dispatch regressed: %+v, ok=%v", usage, ok)
	}
}

// TestTUIEventPipelineDeliversEventUsage 是端到端证明：agent 的用量事件确实穿过 hub 与桥接器，
// 到达前端抽取的 TUI 通道，携带当轮用量而非运行总量。
func TestTUIEventPipelineDeliversEventUsage(t *testing.T) {
	prev := tuiUsageVisible.Swap(true)
	defer tuiUsageVisible.Store(prev)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewTUIEventPipeline(ctx)

	runCtx := withEventEmit(context.Background(), p.Emit)
	EmitEvent(runCtx, agent.Event{
		Kind:       agent.EventUsage,
		Usage:      llm.Usage{PromptTokens: 100, CompletionTokens: 25, CachedTokens: 40},
		TotalUsage: llm.Usage{PromptTokens: 500, CompletionTokens: 125, CachedTokens: 0},
	})

	select {
	case got := <-p.Events:
		want := tui.Event{
			Kind: tui.TuiEventUsage, PromptTokens: 100,
			CompletionTokens: 25, CachedTokens: 40,
		}
		if got != want {
			t.Fatalf("pipelined usage = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EventUsage did not reach the TUI event channel")
	}

	p.Stop()
	select {
	case _, open := <-p.Events:
		if open {
			t.Fatal("pipeline.Events must close after Stop")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline.Events did not close after Stop")
	}
}
