package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestEventIsTerminalOnlyForStop(t *testing.T) {
	if !(Event{Kind: EventStop}).IsTerminal() {
		t.Fatal("EventStop must be terminal (must-deliver)")
	}
	for _, kind := range []EventKind{
		EventTurnStart, EventTurnEnd, EventCheckpointReady,
		EventTextDelta, EventReasoningDelta, EventToolStart, EventToolEnd,
		EventUsage, EventError, EventNotice,
		EventSubagentStart, EventSubagentEnd,
	} {
		if (Event{Kind: kind}).IsTerminal() {
			t.Errorf("kind %q must not be terminal", kind)
		}
	}
}

// TestRunEmitsExactlyOneTerminalAcrossExitPaths 锁定「只保留最后一个终结事件」背后的运行级不变量：循环的众多退出路径
// 每条都会发出自己的 EventStop 并立即返回，因此订阅者每次运行只观察到一个完成事件，无需猜测哪个才是最终的。
// （接上行）
// （接上行）
func TestRunEmitsExactlyOneTerminalAcrossExitPaths(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return textReply("done"), nil
		}}
		assertOneTerminal(t, Options{Model: "m", Chatter: chat}, context.Background())
	})
	t.Run("provider error", func(t *testing.T) {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return llm.Message{}, errors.New("boom")
		}}
		assertOneTerminal(t, Options{Model: "m", Chatter: chat}, context.Background())
	})
	t.Run("cancelled", func(t *testing.T) {
		chat := &fakeChatter{fn: func(int, []llm.Message, []llm.ToolSpec, func(string)) (llm.Message, error) {
			return textReply("done"), nil
		}}
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // 在第一轮之前停止，使循环顶部的中止路径运行
		assertOneTerminal(t, Options{Model: "m", Chatter: chat}, ctx)
	})
}

func assertOneTerminal(t *testing.T, opts Options, ctx context.Context) {
	t.Helper()
	var emitted []Event
	opts.Emit = func(e Event) { emitted = append(emitted, e) }
	_, _ = (&Runner{Opts: opts}).Run(ctx, "hi")

	terminals := 0
	for _, e := range emitted {
		if e.IsTerminal() {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("run emitted %d terminal event(s), want 1: %+v", terminals, emitted)
	}
}
