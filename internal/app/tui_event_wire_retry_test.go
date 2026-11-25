package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/tui"
)

// TestAgentToTUIEventRetryMapsFields 证明每个可安全上线传输的重试标量都到达实时退避渲染器读取的 TUI 字段。
// Turn 和 Model 没有 TUI 对应项，不得在这里凭空编造。
// （接上行）
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

// TestAgentToTUIEventRetryNilDropped 证明没有负载的重试事件会被过滤，而不是在屏幕上产生空的倒计时。
// （接上行）
func TestAgentToTUIEventRetryNilDropped(t *testing.T) {
	got, ok := AgentToTUIEvent(agent.Event{Kind: agent.EventRetry})
	if ok {
		t.Fatalf("retry without Retry payload must not map, got %+v", got)
	}
	if got != (tui.Event{}) {
		t.Fatalf("dropped retry returned a non-zero event: %+v", got)
	}
}

// TestAgentToTUIEventRetryDoesNotRegressOtherKinds 证明分派仍以 e.Kind 为键：其他事件上多余的 Retry 负载既不会改变其映射，
// 也不会让它成为重试，而真正的重试仍映射为自身。
// （接上行）
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

// TestAgentToTUIEventRetryReasonRedactedOnRender 证明映射后的 Reason 可安全显示：即使形似凭据的值泄漏进重试原因，
// TUI 的 RetryNotice 也会将其脱敏，使任何密钥都不会出现在屏幕上。映射本身只复制封闭的 Reason 标记，而绝不复制自由格式的提供方文本。
// （接上行）
// （接上行）
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

// TestTUIEventPipelineDeliversEventRetry 是端到端证明：agent 的重试反馈确实到达前端抽取的 TUI 通道。
// （接上行）
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
