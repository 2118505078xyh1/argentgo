package app

import (
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tui"
)

// TestAgentToTUIEventUsageTiming 证明 llm 打在 Usage 上的三个毫秒时间点被
// 桥接成 TUI 的 FirstTokenAt/StreamedAt/CompletedAt，且顺序保持递增。
func TestAgentToTUIEventUsageTiming(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	in := agent.Event{
		Kind: agent.EventUsage,
		Usage: llm.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			StreamRequestMS:  base.Add(-100 * time.Millisecond).UnixMilli(),
			StreamFirstMS:    base.Add(200 * time.Millisecond).UnixMilli(),
			StreamDoneMS:     base.Add(2200 * time.Millisecond).UnixMilli(),
		},
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("usage event was not mapped")
	}
	if !got.FirstTokenAt.Equal(base.Add(200 * time.Millisecond)) {
		t.Errorf("FirstTokenAt = %v, want %v", got.FirstTokenAt, base.Add(200*time.Millisecond))
	}
	wantStreamed := base.Add(2200 * time.Millisecond)
	if !got.StreamedAt.Equal(wantStreamed) {
		t.Errorf("StreamedAt = %v, want %v", got.StreamedAt, wantStreamed)
	}
	if !got.CompletedAt.Equal(wantStreamed) {
		t.Errorf("CompletedAt = %v, want %v", got.CompletedAt, wantStreamed)
	}
	if !(got.FirstTokenAt.Before(got.StreamedAt) && !got.StreamedAt.After(got.CompletedAt)) {
		t.Errorf("timing points must be non-decreasing: first=%v streamed=%v completed=%v",
			got.FirstTokenAt, got.StreamedAt, got.CompletedAt)
	}
	// token 计数仍照常映射，打点不影响原有字段。
	if got.PromptTokens != 100 || got.CompletionTokens != 50 {
		t.Errorf("token fields changed: %+v", got)
	}
}

// TestAgentToTUIEventUsageMissingTimingFallsBack 证明时间点缺失时三个字段
// 保持零值——TUI 据此回退到相邻 Usage 的速率算法，缺省帧逐字节不变。
func TestAgentToTUIEventUsageMissingTimingFallsBack(t *testing.T) {
	in := agent.Event{
		Kind:  agent.EventUsage,
		Usage: llm.Usage{PromptTokens: 100, CompletionTokens: 50},
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("usage event was not mapped")
	}
	if !got.FirstTokenAt.IsZero() || !got.StreamedAt.IsZero() || !got.CompletedAt.IsZero() {
		t.Fatalf("missing timing must keep zero times: %+v", got)
	}
	want := tui.Event{Kind: tui.TuiEventUsage, PromptTokens: 100, CompletionTokens: 50}
	if got != want {
		t.Fatalf("default frame changed: got %+v, want %+v", got, want)
	}
}

// TestAgentToTUIEventTimingOnlyOnUsage 证明打点字段只在 usage 事件上被读取，
// 其它种类的事件即使带有 Usage 负载也不会凭空产生时间点。
func TestAgentToTUIEventTimingOnlyOnUsage(t *testing.T) {
	in := agent.Event{
		Kind:       agent.EventToolEnd,
		ToolCallID: "c1",
		ToolName:   "bash",
		Usage: llm.Usage{
			StreamRequestMS: 1,
			StreamFirstMS:   2,
			StreamDoneMS:    3,
		},
	}
	got, ok := AgentToTUIEvent(in)
	if !ok {
		t.Fatal("tool_end event was not mapped")
	}
	if !got.FirstTokenAt.IsZero() || !got.StreamedAt.IsZero() || !got.CompletedAt.IsZero() {
		t.Fatalf("non-usage event must not carry timing: %+v", got)
	}
	if got.Kind != tui.EventToolEnd || got.ToolCallID != "c1" || got.ToolName != "bash" {
		t.Fatalf("tool_end mapping regressed: %+v", got)
	}
}
