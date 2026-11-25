package app

import (
	"context"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/tui"
)

// TestAgentToTUIEventCriticalNotices 证明压缩与预算类事件不再被桥接器吞掉：
// 它们映射到专用的 tui.TuiEventNotice，正文原样进入 Detail 供渲染层本地化。
func TestAgentToTUIEventCriticalNotices(t *testing.T) {
	cases := []struct {
		name string
		in   agent.Event
	}{
		{"mid-turn compact", agent.Event{Kind: agent.EventNotice, Text: "compacted context: ~9000 -> ~3000 tokens (target 4000)"}},
		{"compact retry", agent.Event{Kind: agent.EventNotice, Text: "上下文溢出：已将记录从 ~9000 压缩到 ~3000 tokens，重放第 1 轮"}},
		{"compact drain", agent.Event{Kind: agent.EventNotice, Text: "压缩后仍超限，已淘汰 2 条最旧消息，剩余 ~3000 tokens"}},
		{"compact payload", agent.Event{Kind: agent.EventNotice, Text: "压缩请求体积超限：已剥离 3 处媒体/工具输出后重试"}},
		{"budget forecast", agent.Event{Kind: agent.EventNotice, Text: "token budget forecast: nearing limit"}},
		{"budget reminder", agent.Event{Kind: agent.EventBudgetReminder, Text: "[budget] 上下文剩余 1200 tokens"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AgentToTUIEvent(tc.in)
			if !ok {
				t.Fatalf("critical notice must have a TUI mapping: %+v", tc.in)
			}
			if got.Kind != tui.TuiEventNotice || got.Detail != tc.in.Text {
				t.Fatalf("mapped = %+v, want kind %q detail %q", got, tui.TuiEventNotice, tc.in.Text)
			}
			if !isCriticalTUIEvent(got) {
				t.Fatal("mapped notice must be routed through the non-droppable queue")
			}
		})
	}
}

// TestAgentToTUIEventOtherNoticesStillIgnored 守护选择性映射：verify / subagent /
// steering 等普通通知仍无 TUI 映射，避免首屏与 transcript 被无关事件刷屏。
func TestAgentToTUIEventOtherNoticesStillIgnored(t *testing.T) {
	for _, text := range []string{
		"verify: check could not run; skipping reflection",
		"background task started",
		"just a normal notice",
	} {
		if got, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventNotice, Text: text}); ok || got != (tui.Event{}) {
			t.Fatalf("plain notice %q = %+v ok=%v, want unmapped", text, got, ok)
		}
	}
}

// TestStartTUIEventBridgeNeverDropsCritical 证明消费者缓慢、out 已满时关键事件
// 仍被排队而非丢弃：先灌满普通事件通道，再发一条压缩通知，排空后它必须出现。
func TestStartTUIEventBridgeNeverDropsCritical(t *testing.T) {
	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := StartTUIEventBridge(ctx, hub)

	// 先发一条并收到它，确认桥接器已完成订阅，避免依赖 hub 的历史回放上限。
	hub.Publish(agent.Event{Kind: agent.EventTurnStart})
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not deliver the first event")
	}
	// 再把 out 的 256 个槽位填满（此时不消费），随后发关键事件。
	for i := 0; i < tuiEventBridgeCap; i++ {
		hub.Publish(agent.Event{Kind: agent.EventTurnStart})
	}
	const wantDetail = "compacted context: ~9000 -> ~3000 tokens (target 4000)"
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: wantDetail})

	deadline := time.After(2 * time.Second)
	found := false
	for i := 0; i < tuiEventBridgeCap+1; i++ {
		select {
		case got, open := <-ch:
			if !open {
				t.Fatal("bridge closed before critical event was delivered")
			}
			if got.Kind == tui.TuiEventNotice && got.Detail == wantDetail {
				found = true
			}
		case <-deadline:
			t.Fatal("timed out draining bridge")
		}
	}
	if !found {
		t.Fatal("critical compaction notice was dropped when the bridge was full")
	}
}
