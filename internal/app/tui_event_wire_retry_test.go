package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestAgentToTUIEventRetryMapsFields proves every wire-safe retry scalar reaches
// the TUI field the live backoff renderer reads. Turn and Model have no TUI
// counterpart and must not be invented here.
func TestAgentToTUIEventRetryMapsFields(t *testing.T) {
	in := agent.Event{
		Kind: agent.EventRetry,
		Turn: 7,
		Retry: &agent.RetryInfo{
			Attempt:     3,
			MaxAttempts: 5,
			DelayMS:     1500,
			Reason:      "rate_limit",
			Model:       "gpt-x",
		},
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("retry must have a TUI mapping")
	}
	want := tui.Event{
		Kind:         tui.EventRetry,
		RetryReason:  "rate_limit",
		RetryAttempt: 3,
		RetryMax:     5,
		RetryWaitMS:  1500,
	}
	if got != want {
		t.Fatalf("mapped event = %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventRetryNilDropped proves a retry event without its payload
// is filtered instead of producing an empty countdown on screen.
func TestAgentToTUIEventRetryNilDropped(t *testing.T) {
	got, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventRetry})
	if ok {
		t.Fatalf("retry without Retry payload must not map, got %+v", got)
	}
	if got != (tui.Event{}) {
		t.Fatalf("dropped retry returned a non-zero event: %+v", got)
	}
}

// TestAgentToTUIEventRetryDoesNotRegressOtherKinds proves dispatch stays keyed
// on e.Kind: a stray Retry payload on another event neither changes its mapping
// nor makes it a retry, and a real retry still maps to itself.
func TestAgentToTUIEventRetryDoesNotRegressOtherKinds(t *testing.T) {
	stray := &agent.RetryInfo{Attempt: 2, MaxAttempts: 4, DelayMS: 100, Reason: "overloaded"}

	tool, ok := AgentToTUIEvent(agent.Event{
		Kind: agent.EventToolStart, ToolCallID: "call-1", ToolName: "bash", Args: "ls", Retry: stray,
	})
	if !ok || tool.Kind != tui.EventToolStart || tool.RetryReason != "" {
		t.Fatalf("tool_start with a stray Retry payload regressed: %+v, ok=%v", tool, ok)
	}

	errEvt, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventError, Err: "stream reset", Retry: stray})
	if !ok || errEvt.Kind != tui.EventError || errEvt.Detail != "stream reset" {
		t.Fatalf("error with a stray Retry payload regressed: %+v, ok=%v", errEvt, ok)
	}

	retry, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventRetry, Retry: stray})
	if !ok || retry.Kind != tui.EventRetry || retry.RetryReason != "overloaded" {
		t.Fatalf("retry dispatch regressed: %+v, ok=%v", retry, ok)
	}
}

// TestAgentToTUIEventRetryReasonRedactedOnRender proves the mapped Reason is
// safe to display: even if a credential-shaped value leaks into a retry reason,
// the TUI's RetryNotice redacts it so no key reaches the screen. The mapping
// itself only copies the closed Reason token and never free-form provider text.
func TestAgentToTUIEventRetryReasonRedactedOnRender(t *testing.T) {
	const secret = "sk-ant-api03-abcdefghijklmnopqrst"
	in := agent.Event{
		Kind: agent.EventRetry,
		Retry: &agent.RetryInfo{
			Attempt: 1, MaxAttempts: 3, DelayMS: 500,
			Reason: "rate_limit api_key=" + secret,
		},
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("retry must have a TUI mapping")
	}
	rendered := tui.RetryNotice(got.RetryReason, got.RetryAttempt, got.RetryMax, got.RetryWaitMS)
	if strings.Contains(rendered, secret) {
		t.Fatalf("retry notice leaked credential: %q", rendered)
	}
	if !strings.Contains(rendered, "[redacted]") {
		t.Fatalf("retry notice did not mark the credential redacted: %q", rendered)
	}
}

// TestTUIEventPipelineDeliversEventRetry is the end-to-end proof that agent
// retry feedback actually reaches the TUI channel the frontend drains.
func TestTUIEventPipelineDeliversEventRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := NewTUIEventPipeline(ctx)

	runCtx := withEventEmit(context.Background(), p.Emit)
	EmitEvent(runCtx, agent.Event{
		Kind:  agent.EventRetry,
		Retry: &agent.RetryInfo{Attempt: 2, MaxAttempts: 4, DelayMS: 800, Reason: "overloaded"},
	})

	select {
	case got := <-p.Events:
		want := tui.Event{
			Kind: tui.EventRetry, RetryReason: "overloaded",
			RetryAttempt: 2, RetryMax: 4, RetryWaitMS: 800,
		}
		if got != want {
			t.Fatalf("pipelined retry = %+v, want %+v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("EventRetry did not reach the TUI event channel")
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
