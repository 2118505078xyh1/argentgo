package server

import (
	"sync"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// recvTyped 从订阅通道读取一个事件，超时则判定失败。
func recvTyped(t *testing.T, ch <-chan agent.Event) agent.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("等待事件超时")
		return agent.Event{}
	}
}

// expectNoTypedEvent 断言通道在短时间内没有多余事件，用于验证过滤确实丢弃了不关心的类型。
func expectNoTypedEvent(t *testing.T, ch <-chan agent.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("收到不应投递的事件: kind=%q text=%q", ev.Kind, ev.Text)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSubscribeTypedSingleKind 验证单类型过滤：只接收指定 Kind，其它类型（含终态事件）均被丢弃。
func TestSubscribeTypedSingleKind(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped([]string{string(agent.EventTextDelta)})
	defer stop()

	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "first"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, Text: "skip-tool"})
	hub.Publish(agent.Event{Kind: agent.EventStop, Text: "skip-terminal"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "second"})

	for _, want := range []string{"first", "second"} {
		ev := recvTyped(t, ch)
		if ev.Kind != agent.EventTextDelta || ev.Text != want {
			t.Fatalf("事件 = (kind=%q, text=%q), want (text_delta, %q)", ev.Kind, ev.Text, want)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedMultipleKinds 验证多类型过滤：命中集合中任一类型即投递，且保持发布顺序。
func TestSubscribeTypedMultipleKinds(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped([]string{
		string(agent.EventToolStart),
		string(agent.EventToolEnd),
	})
	defer stop()

	hub.Publish(agent.Event{Kind: agent.EventTurnStart, Text: "skip-turn"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, ToolName: "read"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "skip-text"})
	hub.Publish(agent.Event{Kind: agent.EventToolEnd, ToolName: "write"})

	first := recvTyped(t, ch)
	if first.Kind != agent.EventToolStart || first.ToolName != "read" {
		t.Fatalf("首个事件 = (kind=%q, tool=%q), want (tool_start, read)", first.Kind, first.ToolName)
	}
	second := recvTyped(t, ch)
	if second.Kind != agent.EventToolEnd || second.ToolName != "write" {
		t.Fatalf("次个事件 = (kind=%q, tool=%q), want (tool_end, write)", second.Kind, second.ToolName)
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedAllKinds 验证显式列出全部已知类型时，所有事件都会被投递。
func TestSubscribeTypedAllKinds(t *testing.T) {
	all := []agent.EventKind{
		agent.EventTurnStart,
		agent.EventTurnEnd,
		agent.EventCheckpointReady,
		agent.EventTextDelta,
		agent.EventReasoningDelta,
		agent.EventToolStart,
		agent.EventToolEnd,
		agent.EventUsage,
		agent.EventError,
		agent.EventNotice,
		agent.EventStop,
		agent.EventSubagentStart,
		agent.EventSubagentEnd,
		agent.EventRetry,
		agent.EventBudgetReminder,
		agent.EventPartial,
	}
	types := make([]string, len(all))
	for i, kind := range all {
		types[i] = string(kind)
	}

	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped(types)
	defer stop()

	for i, kind := range all {
		hub.Publish(agent.Event{Kind: kind, Text: string(kind)})
		if got := recvTyped(t, ch); got.Kind != kind {
			t.Fatalf("第 %d 个事件 kind = %q, want %q", i, got.Kind, kind)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedPreservesOrder 验证过滤后事件的相对顺序与发布顺序严格一致。
func TestSubscribeTypedPreservesOrder(t *testing.T) {
	hub := NewEventHub()
	const wantKind = agent.EventKind("target")
	ch, stop := hub.SubscribeTyped([]string{string(wantKind)})
	defer stop()

	const total = 100
	for i := 0; i < total; i++ {
		kind := wantKind
		if i%2 == 1 {
			kind = agent.EventNotice // 夹在中间的干扰类型
		}
		hub.Publish(agent.Event{Kind: kind, Text: string(rune('a' + i%26))})
	}

	// 只对偶数序号发布 wantKind，读取顺序必须与发布顺序相同。
	for i := 0; i < total; i += 2 {
		ev := recvTyped(t, ch)
		if ev.Kind != wantKind {
			t.Fatalf("第 %d 次读取 kind = %q, want %q", i, ev.Kind, wantKind)
		}
		if want := string(rune('a' + i%26)); ev.Text != want {
			t.Fatalf("第 %d 次读取 text = %q, want %q", i, ev.Text, want)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedStopIsIdempotent 验证退订：stop 幂等、可并发调用，退订后通道关闭且不再投递。
func TestSubscribeTypedStopIsIdempotent(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.SubscribeTyped([]string{string(agent.EventNotice)})

	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "first"})
	if ev := recvTyped(t, ch); ev.Text != "first" {
		t.Fatalf("事件 text = %q, want first", ev.Text)
	}

	// 并发调用 stop，借助 -race 检测数据竞争；重复调用不得 panic。
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
		}()
	}
	wg.Wait()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("退订后通道仍保持打开")
		}
	case <-time.After(time.Second):
		t.Fatal("退订后通道未关闭")
	}

	// 退订后再发布不应引发向已关闭通道写入的 panic。
	hub.Publish(agent.Event{Kind: agent.EventNotice, Text: "after-stop"})
}

// TestSubscribeTypedNoTypesReceivesAll 验证 types 为 nil 或空切片时不过滤，行为与 Subscribe 一致。
func TestSubscribeTypedNoTypesReceivesAll(t *testing.T) {
	for _, tc := range []struct {
		name  string
		types []string
	}{
		{name: "nil", types: nil},
		{name: "empty", types: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := NewEventHub()
			ch, stop := hub.SubscribeTyped(tc.types)
			defer stop()

			kinds := []agent.EventKind{
				agent.EventNotice,
				agent.EventTextDelta,
				agent.EventStop,
				agent.EventToolStart,
			}
			for _, kind := range kinds {
				hub.Publish(agent.Event{Kind: kind})
			}
			for i, kind := range kinds {
				if got := recvTyped(t, ch); got.Kind != kind {
					t.Fatalf("第 %d 个事件 kind = %q, want %q", i, got.Kind, kind)
				}
			}
			expectNoTypedEvent(t, ch)
		})
	}
}

// TestSubscribeTypedCJKKind 验证 CJK 类型名与内置 EventKind 一样按字符串精确匹配。
func TestSubscribeTypedCJKKind(t *testing.T) {
	hub := NewEventHub()
	const target = "中文事件"
	ch, stop := hub.SubscribeTyped([]string{target})
	defer stop()

	hub.Publish(agent.Event{Kind: agent.EventKind(target), Text: "one"})
	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "skip"})
	hub.Publish(agent.Event{Kind: agent.EventKind("另一种类型"), Text: "skip-too"})
	hub.Publish(agent.Event{Kind: agent.EventKind(target), Text: "two"})

	for _, want := range []string{"one", "two"} {
		ev := recvTyped(t, ch)
		if ev.Kind != agent.EventKind(target) || ev.Text != want {
			t.Fatalf("事件 = (kind=%q, text=%q), want (中文事件, %q)", ev.Kind, ev.Text, want)
		}
	}
	expectNoTypedEvent(t, ch)
}

// TestSubscribeTypedDoesNotAffectNormalSubscribers 验证类型过滤仅作用于该订阅者，
// 普通 Subscribe 订阅者仍接收全部事件。
func TestSubscribeTypedDoesNotAffectNormalSubscribers(t *testing.T) {
	hub := NewEventHub()
	typed, stopTyped := hub.SubscribeTyped([]string{string(agent.EventTextDelta)})
	defer stopTyped()
	normal, stopNormal := hub.Subscribe(0)
	defer stopNormal()

	hub.Publish(agent.Event{Kind: agent.EventTextDelta, Text: "keep"})
	hub.Publish(agent.Event{Kind: agent.EventToolStart, Text: "only-normal"})

	if ev := recvTyped(t, typed); ev.Text != "keep" {
		t.Fatalf("过滤订阅者事件 text = %q, want keep", ev.Text)
	}
	expectNoTypedEvent(t, typed)

	for _, want := range []string{"keep", "only-normal"} {
		if ev := recvTyped(t, normal); ev.Text != want {
			t.Fatalf("普通订阅者事件 text = %q, want %q", ev.Text, want)
		}
	}
}
