package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestAgentToTUIEventToolStart proves a tool_start keeps the fields the TUI
// needs to promote a pending approval card and render a running task row.
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

// TestAgentToTUIEventToolEnd proves tool_end carries the outcome fields the TUI
// uses to close a task row and badge a failed tool entry.
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

// TestAgentToTUIEventError proves a failed request surfaces its redacted error
// text through tui.EventError's Detail field.
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

// TestAgentToTUIEventUnmappedKinds guards the honesty contract: tui.Event has no
// text, notice, usage or stop kind, so those events must not be manufactured.
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
		agent.EventNotice, // plain notice, not an approval
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

// TestAgentToTUIEventApprovalNotice documents that approval notices are
// recognised (via ParseApprovalEvent) but intentionally have no TUI event: the
// approval card is created by Model.Approve and promoted by the later
// tool_start. Emitting one here would show it as running before the answer.
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

// TestStartTUIEventBridgeDeliversInOrder proves the bridge forwards only mapped
// events, in publish order, and filters the rest.
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

// TestStartTUIEventBridgeClosesOnCancel proves cancellation closes the channel
// and the hub subscription, even with no events in flight.
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

// TestStartTUIEventBridgeSlowConsumer proves the out channel never blocks the
// bridge: with more events than the buffer can hold and nobody reading, cancel
// still unwinds the goroutine and closes the channel.
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

// TestTUIEventPipelineDeliversAgentEvents proves the production wiring: an
// EmitEvent call on a run context reaches the TUI channel, and Stop closes it.
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
