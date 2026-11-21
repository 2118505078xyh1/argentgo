package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/eval"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

func makePressureMessages(n int, content string) []eval.MessageLike {
	out := make([]eval.MessageLike, n)
	for i := range out {
		out[i] = eval.MessageLike{Role: "user", Content: content}
	}
	return out
}

// TestPressureNoticeVerdicts 覆盖三个等级以及三个阈值信号（token / 工具字节 / 消息数）。
func TestPressureNoticeVerdicts(t *testing.T) {
	tests := []struct {
		name      string
		messages  []eval.MessageLike
		wantEmpty bool
		wantLabel string // 非空时要求告警里出现的等级标签
	}{
		{name: "healthy", messages: []eval.MessageLike{{Role: "user", Content: "hi"}}, wantEmpty: true},
		{name: "warning tokens", messages: []eval.MessageLike{{Role: "user", Content: strings.Repeat("x", 4000)}}, wantLabel: "警告"},
		{name: "critical tokens", messages: []eval.MessageLike{{Role: "user", Content: strings.Repeat("x", 8000)}}, wantLabel: "危急"},
		{name: "tool bytes threshold", messages: []eval.MessageLike{{Role: "tool", Content: strings.Repeat("x", 20000), ToolResult: true}}},
		{name: "message count threshold", messages: makePressureMessages(21, "x"), wantLabel: "警告"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pressureNotice(tc.messages)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("healthy messages produced notice: %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, "[context pressure] ") {
				t.Fatalf("notice missing stable prefix: %q", got)
			}
			if tc.wantLabel != "" && !strings.Contains(got, tc.wantLabel) {
				t.Fatalf("notice %q missing label %q", got, tc.wantLabel)
			}
		})
	}
}

// TestPressureNoticeCJK 验证 CJK 按 rune 计数，不会因字节膨胀被误判为危急。
func TestPressureNoticeCJK(t *testing.T) {
	// 4000 个汉字 = 4000 rune（warning 阈值）但 12000 字节；若按字节计会被判成 critical。
	got := pressureNotice([]eval.MessageLike{{Role: "user", Content: strings.Repeat("中", 4000)}})
	if got == "" {
		t.Fatal("expected a notice for CJK warning-pressure message")
	}
	if !strings.Contains(got, "警告") {
		t.Fatalf("CJK must be warning (rune-counted), got %q", got)
	}
	if strings.Contains(got, "危急") {
		t.Fatalf("CJK must not be inflated to critical, got %q", got)
	}
	if !strings.Contains(got, "估算 token 4000") {
		t.Fatalf("Summary must report rune-based token count 4000, got %q", got)
	}
	if short := pressureNotice([]eval.MessageLike{{Role: "user", Content: "你好"}}); short != "" {
		t.Fatalf("short CJK message must stay healthy, got %q", short)
	}
}

// TestPressureNoticeDeterministic 验证纯函数：同样输入同样输出，且不修改入参。
func TestPressureNoticeDeterministic(t *testing.T) {
	messages := []eval.MessageLike{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: strings.Repeat("x", 5000)},
		{Role: "tool", Content: strings.Repeat("y", 100), ToolResult: true},
	}
	first := pressureNotice(messages)
	second := pressureNotice(messages)
	if first == "" || first != second {
		t.Fatalf("pressureNotice is not deterministic: %q vs %q", first, second)
	}
	if messages[0].Content != "你好" || messages[2].Content != strings.Repeat("y", 100) {
		t.Fatal("pressureNotice mutated its input")
	}
}

// TestContextPressureObserverGate 验证钩子字段默认 nil 时的空操作：既不构造消息也不发事件。
func TestContextPressureObserverGate(t *testing.T) {
	var emitted []agent.Event
	emit := func(e agent.Event) { emitted = append(emitted, e) }

	// nil 观察器：采集与轮末上报都是空操作。
	var off *contextPressureObserver
	off.collectEvent(agent.Event{Kind: agent.EventTextDelta, Text: strings.Repeat("x", 8000)})
	off.observeStop(emit)
	if len(emitted) != 0 {
		t.Fatalf("nil observer emitted events: %v", emitted)
	}

	// 钩子为 nil 的观察器同样完全静默。
	empty := &contextPressureObserver{}
	empty.collectEvent(agent.Event{Kind: agent.EventTextDelta, Text: strings.Repeat("x", 8000)})
	empty.observeStop(emit)
	if len(emitted) != 0 || len(empty.messages) != 0 {
		t.Fatalf("nil-hook observer not silent: events=%v messages=%d", emitted, len(empty.messages))
	}

	// 挂接钩子后：低压力静默、高压力产生恰好一条 notice。
	on := &contextPressureObserver{notice: pressureNotice}
	on.collectEvent(agent.Event{Kind: agent.EventTextDelta, Text: "hi"})
	on.observeStop(emit)
	if len(emitted) != 0 {
		t.Fatalf("low pressure should be silent, got %v", emitted)
	}
	on.collectEvent(agent.Event{Kind: agent.EventTextDelta, Text: strings.Repeat("x", 5000)})
	on.observeStop(emit)
	if len(emitted) != 1 || emitted[0].Kind != agent.EventNotice {
		t.Fatalf("high pressure must emit exactly one notice, got %v", emitted)
	}
	if len(on.messages) != 0 {
		t.Fatalf("observer must reset accumulated messages after stop, got %d", len(on.messages))
	}
}

// TestContextPressureSwitch 验证开关各值与装配结果。
func TestContextPressureSwitch(t *testing.T) {
	SetContextPressure(false)
	defer SetContextPressure(false) // 恢复默认，避免污染其它测试

	if contextPressureOn() {
		t.Fatal("switch must default to off here")
	}
	if o := newContextPressureObserver(); o != nil {
		t.Fatalf("off state must not attach an observer, got %+v", o)
	}

	SetContextPressure(true)
	if !contextPressureOn() {
		t.Fatal("switch must report on after SetContextPressure(true)")
	}
	o := newContextPressureObserver()
	if o == nil || o.notice == nil {
		t.Fatal("on state must attach an observer with a notice hook")
	}

	SetContextPressure(false)
	if contextPressureOn() || newContextPressureObserver() != nil {
		t.Fatal("switch must detach observer when turned back off")
	}
}

// TestRunnerPromptAssemblesPressureObserver 验证装配点：runner.Prompt 仅当开关打开时
// 才把观察器挂到执行 context 上，关闭时保持 ctx 不含观察器。
func TestRunnerPromptAssemblesPressureObserver(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		enabled      bool
		wantAttached bool
	}{
		{name: "off", enabled: false, wantAttached: false},
		{name: "on", enabled: true, wantAttached: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			SetContextPressure(tc.enabled)
			defer SetContextPressure(false)

			var seen *contextPressureObserver
			r := &runner{
				stdio: cli.IO{Env: env},
				res:   res,
				store: testStore(t, res),
				execute: func(ctx context.Context, _ cli.IO, _ Resolved, _ *session.Session, _ string, _ func(string)) (agent.Result, error) {
					seen = contextPressureOf(ctx)
					return agent.Result{Text: "ok"}, nil
				},
			}
			if _, err := r.Prompt(context.Background(), "hi", nil); err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			if tc.wantAttached {
				if seen == nil || seen.notice == nil {
					t.Fatal("switch on but no observer attached to execute context")
				}
			} else if seen != nil {
				t.Fatal("switch off but observer attached to execute context")
			}
		})
	}
}

// capturePressureStream 通过真实的 EmitEvent 路径把事件同步发布到 hub，记录每条事件的 seq，
// 并在轮末调用观察器；返回被订阅者观察到的事件序列与对应的 seq 序列。
func capturePressureStream(observer *contextPressureObserver, events []agent.Event) ([]agent.Event, []uint64, uint64) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()
	ctx := withEventEmit(context.Background(), func(e agent.Event) { hub.Publish(e) })
	ctx = withContextPressure(ctx, observer)
	obs := contextPressureOf(ctx)

	seqs := make([]uint64, 0, len(events))
	for _, ev := range events {
		EmitEvent(ctx, ev)
		seqs = append(seqs, hub.Seq())
		obs.collectEvent(ev)
	}
	obs.observeStop(func(e agent.Event) { EmitEvent(ctx, e) })
	finalSeq := hub.Seq()

	var got []agent.Event
	for {
		select {
		case e := <-ch:
			got = append(got, e)
		default:
			return got, seqs, finalSeq
		}
	}
}

// TestContextPressureOffKeepsEventStreamByteIdentical 守护核心契约：关闭时事件序列
// （含 seq）与基线逐字节一致。
func TestContextPressureOffKeepsEventStreamByteIdentical(t *testing.T) {
	SetContextPressure(false)
	defer SetContextPressure(false)

	base := []agent.Event{
		{Kind: agent.EventTurnStart, Turn: 1},
		{Kind: agent.EventTextDelta, Turn: 1, Text: "hello"},
		{Kind: agent.EventToolStart, Turn: 1, ToolName: "read"},
		{Kind: agent.EventToolEnd, Turn: 1, ToolName: "read", Output: "ok"},
		{Kind: agent.EventUsage, Turn: 1},
		{Kind: agent.EventStop, Reason: agent.StopComplete},
	}
	gotBase, seqBase, finalBase := capturePressureStream(nil, base)
	gotOff, seqOff, finalOff := capturePressureStream(newContextPressureObserver(), base)

	if !reflect.DeepEqual(gotOff, gotBase) {
		t.Fatalf("off-state event bytes changed:\n got %+v\nwant %+v", gotOff, gotBase)
	}
	if !reflect.DeepEqual(seqOff, seqBase) || finalOff != finalBase {
		t.Fatalf("off-state seq changed: seqs %v/%v, final %d/%d", seqOff, seqBase, finalOff, finalBase)
	}
}

// TestContextPressureOnAppendsExactlyOneNotice 覆盖三种高压力信号：开启时既有事件字节不变，
// 且恰好追加一条 notice；低压力不追加。
func TestContextPressureOnAppendsExactlyOneNotice(t *testing.T) {
	high := []struct {
		name   string
		events []agent.Event
	}{
		{
			name: "tokens",
			events: []agent.Event{
				{Kind: agent.EventTurnStart, Turn: 1},
				{Kind: agent.EventTextDelta, Turn: 1, Text: strings.Repeat("x", 5000)},
				{Kind: agent.EventStop, Reason: agent.StopComplete},
			},
		},
		{
			name: "tool bytes",
			events: []agent.Event{
				{Kind: agent.EventToolEnd, Turn: 1, ToolName: "read", Output: strings.Repeat("x", 20000)},
				{Kind: agent.EventStop, Reason: agent.StopComplete},
			},
		},
		{
			name: "message count",
			events: func() []agent.Event {
				out := make([]agent.Event, 0, 22)
				for i := 0; i < 21; i++ {
					out = append(out, agent.Event{Kind: agent.EventTextDelta, Turn: 1, Text: "x"})
				}
				return append(out, agent.Event{Kind: agent.EventStop, Reason: agent.StopComplete})
			}(),
		},
	}
	for _, tc := range high {
		t.Run(tc.name, func(t *testing.T) {
			observer := &contextPressureObserver{notice: pressureNotice}
			got, seqs, finalSeq := capturePressureStream(observer, tc.events)
			if len(got) != len(tc.events)+1 {
				t.Fatalf("want exactly one appended notice, got %d events (%d seqs): %v", len(got), len(seqs), got)
			}
			if !reflect.DeepEqual(got[:len(tc.events)], tc.events) {
				t.Fatalf("existing event bytes changed:\n got %+v\nwant %+v", got[:len(tc.events)], tc.events)
			}
			notice := got[len(tc.events)]
			if notice.Kind != agent.EventNotice || !strings.HasPrefix(notice.Text, "[context pressure] ") {
				t.Fatalf("appended event is not a pressure notice: %+v", notice)
			}
			// notice 的 seq 恰好多一个：既有事件的 seq 列表不变，末尾再 +1。
			if want := uint64(len(tc.events) + 1); finalSeq != want {
				t.Fatalf("final seq = %d, want %d (one extra notice)", finalSeq, want)
			}
		})
	}

	// 低压力：挂接钩子也不追加事件。
	low := []agent.Event{
		{Kind: agent.EventTextDelta, Turn: 1, Text: "hi"},
		{Kind: agent.EventStop, Reason: agent.StopComplete},
	}
	gotLow, _, _ := capturePressureStream(&contextPressureObserver{notice: pressureNotice}, low)
	if !reflect.DeepEqual(gotLow, low) {
		t.Fatalf("low pressure must not append events: got %+v, want %+v", gotLow, low)
	}
}
