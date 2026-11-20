package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestAgentToTUIEventToolStart 证明 tool_start 保留了 TUI 提升待审批卡片和渲染运行中任务行所需的字段。
// （接上行）
func TestAgentToTUIEventToolStart(t *testing.T) {
	in := agent.Event{
		Kind:       agent.EventToolStart,
		ToolCallID: "call-7",
		ToolName:   "bash",
		Args:       `{"command":"ls -la"}`,
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("tool_start must have a TUI mapping")
	}
	want := tui.Event{
		Kind:       tui.EventToolStart,
		ToolCallID: "call-7",
		ToolName:   "bash",
		Args:       `{"command":"ls -la"}`,
	}
	if got != want {
		t.Fatalf("mapped event = %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventToolEnd 证明 tool_end 携带 TUI 关闭任务行并标记失败工具条目所需的结果字段。
// （接上行）
func TestAgentToTUIEventToolEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		isError bool
	}{
		{name: "ok", isError: false},
		{name: "error", isError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := agent.Event{
				Kind:       agent.EventToolEnd,
				ToolCallID: "call-9",
				ToolName:   "write",
				Args:       `{"path":"a.go"}`,
				Output:     "done",
				IsError:    tc.isError,
				DurationMS: 42,
			}
			got, ok := AgentToTUIEvent(in)
			if !ok {
				t.Fatal("tool_end must have a TUI mapping")
			}
			want := tui.Event{
				Kind:       tui.EventToolEnd,
				ToolCallID: "call-9",
				ToolName:   "write",
				Args:       `{"path":"a.go"}`,
				Output:     "done",
				IsError:    tc.isError,
				DurationMS: 42,
			}
			if got != want {
				t.Fatalf("mapped event = %+v, want %+v", got, want)
			}
		})
	}
}

// TestAgentToTUIEventError 证明失败的请求通过 tui.EventError 的 Detail 字段呈现其脱敏错误文本。
// （接上行）
func TestAgentToTUIEventError(t *testing.T) {
	got, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventError, Err: "stream reset"})
	if !ok {
		t.Fatal("error must have a TUI mapping")
	}
	want := tui.Event{Kind: tui.EventError, Detail: "stream reset"}
	if got != want {
		t.Fatalf("mapped event = %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventUnmappedKinds 守护诚实契约：tui.Event 没有文本、通知、用量或停止种类，因此不得伪造这些事件。
// （接上行）
func TestAgentToTUIEventUnmappedKinds(t *testing.T) {
	kinds := []agent.EventKind{
		agent.EventTurnStart,
		agent.EventTurnEnd,
		agent.EventCheckpointReady,
		agent.EventTextDelta,
		agent.EventReasoningDelta,
		agent.EventUsage,
		agent.EventStop,
		agent.EventSubagentStart,
		agent.EventSubagentEnd,
		agent.EventNotice, // 普通通知，不是审批
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			got, ok := AgentToTUIEvent(agent.Event{Kind: kind, Text: "payload"})
			if ok {
				t.Fatalf("%s has no TUI EventKind, must not map", kind)
			}
			if got != (tui.Event{}) {
				t.Fatalf("unmapped %s returned a non-zero event: %+v", kind, got)
			}
		})
	}
}

// TestAgentToTUIEventApprovalNotice 记录审批通知会被识别（通过 ParseApprovalEvent），但有意没有 TUI 事件：
// 审批卡片由 Model.Approve 创建，并由之后的 tool_start 提升。
// 在这里发出会使其在回答之前显示为运行中。
// （接上行）
func TestAgentToTUIEventApprovalNotice(t *testing.T) {
	notices := []agent.Event{
		agent.ApprovalRequestedEvent("bash", "digest", "call-1"),
		agent.ApprovalResolvedEvent("bash", "call-1", true),
		agent.ApprovalResolvedEvent("bash", "call-1", false),
	}
	for i, e := range notices {
		kind, tool, id, _, ok := agent.ParseApprovalEvent(e)
		if !ok || kind == "" || tool != "bash" || id != "call-1" {
			t.Fatalf("notice %d not recognised as an approval: kind=%q tool=%q id=%q ok=%v", i, kind, tool, id, ok)
		}
		got, mapped := AgentToTUIEvent(e)
		if mapped {
			t.Fatalf("notice %d must not synthesize a TUI event, got %+v", i, got)
		}
		if got != (tui.Event{}) {
			t.Fatalf("notice %d returned a non-zero event: %+v", i, got)
		}
	}
}

// TestStartTUIEventBridgeDeliversInOrder 证明桥接器只按发布顺序转发映射后的事件，并过滤其余事件。
// （接上行）
func TestStartTUIEventBridgeDeliversInOrder(t *testing.T) {
	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := StartTUIEventBridge(ctx, hub)

	hub.Publish(agent.Event{Kind: agent.EventToolStart, ToolCallID: "a", ToolName: "bash", Args: "ls"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "thinking"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, ToolCallID: "b", ToolName: "read", Args: "a.go"})
	hub.Publish(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "a", ToolName: "bash", IsError: true, DurationMS: 5})
	hub.Publish(agent.Event{Kind: agent.EventUsage})
	hub.Publish(agent.Event{Kind: agent.EventToolEnd, ToolCallID: "b", ToolName: "read", DurationMS: 2})

	want := []tui.Event{
		{Kind: tui.EventToolStart, ToolCallID: "a", ToolName: "bash", Args: "ls"},
		{Kind: tui.EventToolStart, ToolCallID: "b", ToolName: "read", Args: "a.go"},
		{Kind: tui.EventToolEnd, ToolCallID: "a", ToolName: "bash", IsError: true, DurationMS: 5},
		{Kind: tui.EventToolEnd, ToolCallID: "b", ToolName: "read", DurationMS: 2},
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
			t.Fatalf("event %d was not delivered", i)
		}
	}
}

// TestStartTUIEventBridgeClosesOnCancel 证明取消会关闭通道和 hub 订阅，即使没有在途事件。
// （接上行）
func TestStartTUIEventBridgeClosesOnCancel(t *testing.T) {
	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	ch := StartTUIEventBridge(ctx, hub)
	cancel()

	select {
	case _, open := <-ch:
		if open {
			t.Fatal("bridge must close its channel on cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bridge channel did not close after cancel")
	}
}

// TestStartTUIEventBridgeSlowConsumer 证明 out 通道永不阻塞桥接器：当事件多于缓冲区容量且无人读取时，
// 取消仍能展开 goroutine 并关闭通道。
// （接上行）
func TestStartTUIEventBridgeSlowConsumer(t *testing.T) {
	hub := NewEventHub()
	ctx, cancel := context.WithCancel(context.Background())
	ch := StartTUIEventBridge(ctx, hub)

	for i := 0; i < tuiEventBridgeCap+64; i++ {
		hub.Publish(agent.Event{
			Kind:       agent.EventToolStart,
			ToolCallID: fmt.Sprintf("call-%d", i),
			ToolName:   "bash",
		})
	}
	if evicted := hub.Evicted(); evicted != 0 {
		t.Fatalf("hub evicted %d subscribers; the bridge must keep draining", evicted)
	}
	cancel()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-ch:
			if !open {
				return
			}
		case <-deadline:
			t.Fatal("bridge did not unblock and close with a full output buffer")
		}
	}
}

// TestTUIEventPipelineDeliversAgentEvents 证明生产接线：运行 context 上的一次 EmitEvent 调用会到达 TUI 通道，且 Stop 会关闭它。
// （接上行）
func TestTUIEventPipelineDeliversAgentEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewTUIEventPipeline(ctx)

	runCtx := withEventEmit(context.Background(), p.Emit)
	EmitEvent(runCtx, agent.Event{
		Kind:       agent.EventToolStart,
		ToolCallID: "call-1",
		ToolName:   "bash",
		Args:       `{"command":"echo hi"}`,
	})

	select {
	case got := <-p.Events:
		want := (tui.Event{
			Kind:       tui.EventToolStart,
			ToolCallID: "call-1",
			ToolName:   "bash",
			Args:       `{"command":"echo hi"}`,
		})
		if got != want {
			t.Fatalf("pipelined event = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EmitEvent did not reach the TUI event channel")
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
