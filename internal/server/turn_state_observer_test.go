package server

import (
	"sync"
	"testing"
)

// turnStateEvent 是喂给观测回调的一条事件四元组，与 Reduce 参数一一对应。
type turnStateEvent struct {
	kind      string
	turn      int
	startedMS uint64
	seq       uint64
}

// fakeTurnStateSink 是实现可选接口 TurnStateObserverSink 的最小事件源替身。
type fakeTurnStateSink struct {
	observer TurnStateEmit
	calls    int
}

func (s *fakeTurnStateSink) SetTurnStateObserver(o TurnStateEmit) {
	s.observer = o
	s.calls++
}

// TestTurnStateObserverMapsEventsToState 表驱动验证观测回调把事件序列折叠为期望 State。
// 覆盖流式、工具、审批、轮次边界与终止等映射，所有 State 的比较都是逐字段的。
func TestTurnStateObserverMapsEventsToState(t *testing.T) {
	cases := []struct {
		name   string
		events []turnStateEvent
		want   State
	}{
		{
			name: "stream then stop finishes",
			events: []turnStateEvent{
				{EvTurnStart, 1, 1000, 1},
				{EvTextDelta, 1, 0, 2},
				{EvStop, 1, 0, 3},
			},
			want: State{Phase: PhaseFinished, AnchorMS: 1000, Seq: 3},
		},
		{
			name: "tool round trip",
			events: []turnStateEvent{
				{EvTurnStart, 1, 1000, 1},
				{EvToolStart, 1, 1100, 2},
				{EvToolEnd, 1, 1200, 3},
			},
			want: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 3},
		},
		{
			name: "approval round trip",
			events: []turnStateEvent{
				{EvTurnStart, 1, 1000, 1},
				{EvApprovalRequest, 1, 0, 2},
				{EvApprovalResolved, 1, 0, 3},
			},
			want: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 3},
		},
		{
			name: "turn end is idle boundary not terminal",
			events: []turnStateEvent{
				{EvTurnStart, 1, 1000, 1},
				{EvTurnEnd, 1, 1500, 2},
			},
			want: State{Phase: PhaseIdle, AnchorMS: 1000, Seq: 2},
		},
		{
			name: "new turn refreshes anchor",
			events: []turnStateEvent{
				{EvTurnStart, 1, 1000, 1},
				{EvTurnEnd, 1, 1500, 2},
				{EvTurnStart, 2, 2000, 3},
			},
			want: State{Phase: PhaseStreaming, AnchorMS: 2000, Seq: 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s Store
			observer := NewTurnStateObserver(&s)
			for i, ev := range tc.events {
				observer(ev.kind, ev.turn, ev.startedMS, ev.seq)
				if got := s.Load(); got.Seq != ev.seq {
					t.Fatalf("step %d (%s): seq = %d, want %d (state %+v)", i, ev.kind, got.Seq, ev.seq, got)
				}
			}
			if got := s.Load(); got != tc.want {
				t.Fatalf("state = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestTurnStateObserverStaleEventsCulled 表驱动验证观测回调把陈旧事件交给
// Reduce 后静默淘汰：状态逐字段保持不变，且不产生任何错误出口。
func TestTurnStateObserverStaleEventsCulled(t *testing.T) {
	// 先把 store 推到 {streaming, anchor=1000, seq=5}。
	setup := turnStateEvent{EvTurnStart, 1, 1000, 5}
	prev := State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 5}
	cases := []struct {
		name  string
		stale turnStateEvent
	}{
		{"duplicate seq", turnStateEvent{EvTextDelta, 1, 0, 5}},
		{"older seq", turnStateEvent{EvToolStart, 1, 0, 3}},
		{"zero seq", turnStateEvent{EvToolStart, 1, 0, 0}},
		{"stale terminal by seq", turnStateEvent{EvStop, 1, 0, 4}},
		{"older anchor", turnStateEvent{EvToolStart, 1, 900, 6}},
		{"older delta anchor", turnStateEvent{EvTextDelta, 1, 999, 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s Store
			observer := NewTurnStateObserver(&s)
			observer(setup.kind, setup.turn, setup.startedMS, setup.seq)
			if got := s.Load(); got != prev {
				t.Fatalf("setup state = %+v, want %+v", got, prev)
			}
			observer(tc.stale.kind, tc.stale.turn, tc.stale.startedMS, tc.stale.seq)
			if got := s.Load(); got != prev {
				t.Fatalf("stale event changed state: got %+v, want %+v", got, prev)
			}
			// 陈旧事件之后的合法事件仍能继续推进，证明淘汰没有锁死状态。
			observer(EvTextDelta, 1, 0, 8)
			want := State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 8}
			if got := s.Load(); got != want {
				t.Fatalf("after fresh event = %+v, want %+v", got, want)
			}
		})
	}
}

// TestTurnStateObserverSwallowsIllegalTransition 检查观测回调从不把非法转换
// 变成对外可见的错误：终止后的事件与未知 kind 都被丢弃，状态保持不变。
func TestTurnStateObserverSwallowsIllegalTransition(t *testing.T) {
	var s Store
	observer := NewTurnStateObserver(&s)
	observer(EvStop, 1, 0, 1)
	finished := s.Load()
	if finished.Phase != PhaseFinished {
		t.Fatalf("phase = %v, want finished", finished.Phase)
	}
	observer(EvTextDelta, 1, 0, 2)
	if got := s.Load(); got != finished {
		t.Fatalf("post-finish event changed state: got %+v, want %+v", got, finished)
	}

	var unknown Store
	NewTurnStateObserver(&unknown)("bogus_kind", 1, 0, 1)
	if got := unknown.Load(); got != (State{}) {
		t.Fatalf("unknown kind changed state: got %+v, want zero State", got)
	}
}

// TestTurnStateObserverConcurrentApplyLoad 通过观测回调并发 Apply（各自唯一
// 递增 seq）并并发 Load，验证 CAS 不丢更新且读者看到的 seq 单调不减。
// 配合 -race 检查数据竞争。
func TestTurnStateObserverConcurrentApplyLoad(t *testing.T) {
	const n = 200
	var s Store
	observer := NewTurnStateObserver(&s)
	var wg sync.WaitGroup

	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			observer(EvTextDelta, 1, 0, seq)
		}(uint64(i))
	}

	readers := 4
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last uint64
			for i := 0; i < n; i++ {
				cur := s.Load()
				if cur.Seq < last {
					t.Errorf("Load observed seq went backwards: %d -> %d", last, cur.Seq)
				}
				last = cur.Seq
			}
		}()
	}

	wg.Wait()
	final := s.Load()
	if final.Seq != n {
		t.Fatalf("final seq = %d, want %d", final.Seq, n)
	}
	if final.Phase != PhaseStreaming {
		t.Fatalf("final phase = %v, want streaming", final.Phase)
	}
}

// TestTurnStateObserverZeroValue 检查零值 Store 可用、nil store 的空回调不 panic。
func TestTurnStateObserverZeroValue(t *testing.T) {
	var s Store
	if got := s.Load(); got != (State{}) {
		t.Fatalf("zero Store.Load() = %+v, want zero State", got)
	}
	observer := NewTurnStateObserver(&s)
	observer(EvTurnStart, 1, 0, 1)
	want := State{Phase: PhaseStreaming, Seq: 1}
	if got := s.Load(); got != want {
		t.Fatalf("after observer = %+v, want %+v", got, want)
	}

	// nil store 返回空回调，调用它不应 panic。
	noop := NewTurnStateObserver(nil)
	noop(EvStop, 1, 0, 1)
	if AttachTurnStateObserver(&fakeTurnStateSink{}, nil) {
		t.Fatal("AttachTurnStateObserver with nil store must return false")
	}
}

// TestAttachTurnStateObserver 表驱动验证可选接口的类型断言注入路径：
// 实现者收到可用的观测回调，未实现者/ nil sink 保持零副作用。
func TestAttachTurnStateObserver(t *testing.T) {
	t.Run("sink receives working observer", func(t *testing.T) {
		var s Store
		sink := &fakeTurnStateSink{}
		if !AttachTurnStateObserver(sink, &s) {
			t.Fatal("AttachTurnStateObserver returned false for a sink")
		}
		if sink.calls != 1 || sink.observer == nil {
			t.Fatalf("sink calls = %d, observer nil = %v; want 1 and non-nil", sink.calls, sink.observer == nil)
		}
		sink.observer(EvTurnStart, 1, 1000, 1)
		want := State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 1}
		if got := s.Load(); got != want {
			t.Fatalf("state = %+v, want %+v", got, want)
		}
	})

	cases := []struct {
		name string
		sink any
	}{
		{"plain struct", struct{}{}},
		{"nil sink", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is no-op", func(t *testing.T) {
			var s Store
			if AttachTurnStateObserver(tc.sink, &s) {
				t.Fatal("AttachTurnStateObserver returned true for a non-sink")
			}
			if got := s.Load(); got != (State{}) {
				t.Fatalf("store changed for non-sink: %+v", got)
			}
		})
	}
}

// TestTurnStateObserverDoesNotPublishEvents 证明观测回调对事件序列零影响：
// 反复调用它既不推进 hub 的序号，也不向订阅者投递任何事件。
func TestTurnStateObserverDoesNotPublishEvents(t *testing.T) {
	hub := NewEventHub()
	ch, stop := hub.Subscribe(0)
	defer stop()

	observer := NewTurnStateObserver(&Store{})
	for i := 1; i <= 5; i++ {
		observer(EvTextDelta, 1, 0, uint64(i))
	}
	if got := hub.Seq(); got != 0 {
		t.Fatalf("hub.Seq() = %d, want 0: observer must not publish events", got)
	}
	select {
	case ev := <-ch:
		t.Fatalf("observer delivered an event to subscriber: %+v", ev)
	default:
	}
}
