package app

import (
	"context"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/tui"
)

// TestAgentToTUIEventTurnBoundary 证明轮次边界被映射为统计行消费的 TUI 事件，
// 且不携带 agent 专有的 Turn 字段（TUI 事件没有该字段）。
func TestAgentToTUIEventTurnBoundary(t *testing.T) {
	for _, tc := range []struct {
		kind agent.EventKind
		want tui.EventKind
	}{
		{agent.EventTurnStart, "turn_start"},
		{agent.EventTurnEnd, "turn_end"},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			got, ok := AgentToTUIEvent(agent.Event{Kind: tc.kind, Turn: 7})
			if !ok {
				t.Fatalf("%s must have a TUI mapping", tc.kind)
			}
			if want := (tui.Event{Kind: tc.want}); got != want {
				t.Fatalf("mapped event = %+v, want %+v", got, want)
			}
		})
	}
}

// TestAgentToTUIEventNoticeToolProgress 证明工具阶段通知被转成 TUI tool_progress
// 事件：显式标记、大小写变体与裸阶段行都能提取出可读标签。
func TestAgentToTUIEventNoticeToolProgress(t *testing.T) {
	for _, text := range []string{
		"tool_progress: 正在索引文件",
		"TOOL_PROGRESS 读取配置",
		"bash job finished: job-3",
	} {
		t.Run(text, func(t *testing.T) {
			got, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventNotice, Text: text})
			if !ok {
				t.Fatalf("tool progress notice %q must have a TUI mapping", text)
			}
			if got.Kind != tui.EventToolProgress {
				t.Fatalf("kind = %q, want %q", got.Kind, tui.EventToolProgress)
			}
			if label, parsed := tui.ParseToolProgress(text); !parsed || got.Detail != label {
				t.Fatalf("detail = %q, want parsed label %q (ok=%v)", got.Detail, label, parsed)
			}
		})
	}
}

// TestAgentToTUIEventNoticePlainUnmapped 证明新增的进度识别没有波及普通通知与审批通知：
// 前者仍无 TUI 映射，后者仍被有意跳过（卡片由 Model.Approve 渲染）。
func TestAgentToTUIEventNoticePlainUnmapped(t *testing.T) {
	plain, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventNotice, Text: "just a normal notice"})
	if ok || plain != (tui.Event{}) {
		t.Fatalf("plain notice must stay unmapped, got %+v ok=%v", plain, ok)
	}
	for i, e := range []agent.Event{
		agent.ApprovalRequestedEvent("bash", "digest", "call-1"),
		agent.ApprovalResolvedEvent("bash", "call-1", true),
	} {
		got, mapped := AgentToTUIEvent(e)
		if mapped || got != (tui.Event{}) {
			t.Fatalf("approval notice %d must stay unmapped, got %+v ok=%v", i, got, mapped)
		}
	}
}

// fakeUsageVisibility 是 SetUsageVisible 能力的最小假实现，用来守护门控下沉契约。
type fakeUsageVisibility struct {
	visible bool
	called  int
}

func (f *fakeUsageVisibility) SetUsageVisible(v bool) {
	f.visible = v
	f.called++
}

// TestSetTUIUsageVisibleDelegatesToModel 证明门控通过约定的 SetUsageVisible 方法
// 下沉到渲染层；没有该能力的实现被安全跳过（tui API 并行补齐期间的兼容行为）。
func TestSetTUIUsageVisibleDelegatesToModel(t *testing.T) {
	f := &fakeUsageVisibility{}
	setTUIUsageVisible(f, true)
	if f.called != 1 || !f.visible {
		t.Fatalf("setter not delegated: called=%d visible=%v", f.called, f.visible)
	}
	setTUIUsageVisible(f, false)
	if f.called != 2 || f.visible {
		t.Fatalf("setter did not forward false: called=%d visible=%v", f.called, f.visible)
	}
	// 没有该能力的实现：不 panic、不产生可观察副作用。
	setTUIUsageVisible(struct{}{}, true)
}

// TestStartTUIEventBridgeForwardsUsageWithGateOff 证明 opt-in 门控下沉后：默认关闭时
// usage 事件仍被桥接转发，统计行才能拿到 token；是否显示由渲染层的 SetUsageVisible 决定。
func TestStartTUIEventBridgeForwardsUsageWithGateOff(t *testing.T) {
	prev := tuiUsageVisible.Swap(false)
	defer tuiUsageVisible.Store(prev)

	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := StartTUIEventBridge(ctx, hub)

	hub.Publish(agent.Event{Kind: agent.EventUsage, Usage: llm.Usage{
		PromptTokens: 12, CompletionTokens: 3, CachedTokens: 4,
	}})
	select {
	case got, open := <-ch:
		if !open {
			t.Fatal("bridge channel closed before usage event")
		}
		want := tui.Event{Kind: tui.TuiEventUsage, PromptTokens: 12, CompletionTokens: 3, CachedTokens: 4}
		if got != want {
			t.Fatalf("usage event = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("usage event was not forwarded with the gate off")
	}
}

// TestStartTUIEventBridgeForwardsTurnAndProgressWithGateOff 证明轮次边界与工具进度
// 不受用量门控影响：默认关闭时照样转发给 TUI。
func TestStartTUIEventBridgeForwardsTurnAndProgressWithGateOff(t *testing.T) {
	prev := tuiUsageVisible.Swap(false)
	defer tuiUsageVisible.Store(prev)

	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := StartTUIEventBridge(ctx, hub)

	hub.Publish(agent.Event{Kind: agent.EventTurnStart, Turn: 1})
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "tool_progress: 编译中"})
	hub.Publish(agent.Event{Kind: agent.EventTurnEnd, Turn: 1})

	want := []tui.Event{
		{Kind: "turn_start"},
		{Kind: tui.EventToolProgress, Detail: "编译中"},
		{Kind: "turn_end"},
	}
	for i, expected := range want {
		select {
		case got, open := <-ch:
			if !open {
				t.Fatalf("channel closed before event %d", i)
			}
			if got != expected {
				t.Fatalf("event %d = %+v, want %+v", i, got, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d was not delivered with the gate off", i)
		}
	}
}
