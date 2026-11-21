package server

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// TestTurnStatePhaseString 检查所有 phase 的稳定英文标识，未知值统一为 "unknown"。
func TestTurnStatePhaseString(t *testing.T) {
	cases := []struct {
		phase Phase
		want  string
	}{
		{PhaseIdle, "idle"},
		{PhaseStreaming, "streaming"},
		{PhaseAwaitingTool, "awaiting_tool"},
		{PhaseAwaitingApproval, "awaiting_approval"},
		{PhaseFinished, "finished"},
		{Phase(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.phase.String(); got != tc.want {
			t.Errorf("Phase(%d).String() = %q, want %q", tc.phase, got, tc.want)
		}
	}
}

// TestTurnStateReduceLegalTransitions 覆盖合法转换；seq 严格递增且时间戳不早于锚点。
func TestTurnStateReduceLegalTransitions(t *testing.T) {
	cases := []struct {
		name      string
		prev      State
		kind      string
		turn      int
		startedMS uint64
		seq       uint64
		want      State
	}{
		{
			name: "turn_start enters streaming and sets anchor", prev: State{},
			kind: EvTurnStart, turn: 1, startedMS: 1000, seq: 1,
			want: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 1},
		},
		{
			name: "tool_start awaits tool", prev: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 1},
			kind: EvToolStart, turn: 1, startedMS: 1100, seq: 2,
			want: State{Phase: PhaseAwaitingTool, AnchorMS: 1000, Seq: 2},
		},
		{
			name: "tool_end returns to streaming", prev: State{Phase: PhaseAwaitingTool, AnchorMS: 1000, Seq: 2},
			kind: EvToolEnd, turn: 1, startedMS: 1200, seq: 3,
			want: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 3},
		},
		{
			name: "approval request awaits approval", prev: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 3},
			kind: EvApprovalRequest, turn: 1, startedMS: 0, seq: 4,
			want: State{Phase: PhaseAwaitingApproval, AnchorMS: 1000, Seq: 4},
		},
		{
			name: "approval resolved resumes streaming", prev: State{Phase: PhaseAwaitingApproval, AnchorMS: 1000, Seq: 4},
			kind: EvApprovalResolved, turn: 1, startedMS: 0, seq: 5,
			want: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 5},
		},
		{
			name: "progress event keeps streaming", prev: State{Phase: PhaseAwaitingTool, AnchorMS: 1000, Seq: 5},
			kind: EvUsage, turn: 1, startedMS: 0, seq: 6,
			want: State{Phase: PhaseAwaitingTool, AnchorMS: 1000, Seq: 6},
		},
		{
			name: "progress from idle enters streaming", prev: State{Seq: 6},
			kind: EvTextDelta, turn: 1, startedMS: 0, seq: 7,
			want: State{Phase: PhaseStreaming, Seq: 7},
		},
		{
			name: "stop finishes", prev: State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 7},
			kind: EvStop, turn: 1, startedMS: 0, seq: 8,
			want: State{Phase: PhaseFinished, AnchorMS: 1000, Seq: 8},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Reduce(tc.prev, tc.kind, tc.turn, tc.startedMS, tc.seq)
			if err != nil {
				t.Fatalf("Reduce returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Reduce = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestTurnStateReduceIllegalTransitions 检查非法转换返回哨兵错误且状态不变。
func TestTurnStateReduceIllegalTransitions(t *testing.T) {
	finished := State{Phase: PhaseFinished, AnchorMS: 1000, Seq: 5}
	cases := []struct {
		name string
		prev State
		kind string
	}{
		{"progress after finished", finished, EvTextDelta},
		{"stop after finished", finished, EvStop},
		{"turn_start after finished", finished, EvTurnStart},
		{"tool after finished", finished, EvToolStart},
		{"unknown kind", State{Phase: PhaseStreaming, Seq: 5}, "bogus_kind"},
		{"empty kind", State{Seq: 5}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Reduce(tc.prev, tc.kind, 1, 0, tc.prev.Seq+1)
			if !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("error = %v, want errors.Is ErrIllegalTransition", err)
			}
			if got != tc.prev {
				t.Fatalf("state changed on illegal transition: got %+v, want %+v", got, tc.prev)
			}
		})
	}
}

// TestTurnStateReduceStaleSeq 检查 seq 不严格递增的事件被静默淘汰（不报错、状态不变）。
func TestTurnStateReduceStaleSeq(t *testing.T) {
	prev := State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 5}
	cases := []struct {
		name string
		kind string
		seq  uint64
	}{
		{"equal seq", EvTextDelta, 5},
		{"older seq", EvTextDelta, 3},
		{"zero seq", EvToolStart, 0},
		{"stale terminal is still discarded", EvStop, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Reduce(prev, tc.kind, 1, 2000, tc.seq)
			if err != nil {
				t.Fatalf("stale event returned error: %v", err)
			}
			if got != prev {
				t.Fatalf("stale event changed state: got %+v, want %+v", got, prev)
			}
		})
	}
}

// TestTurnStateReduceStaleAnchor 检查带时间戳的上一轮迟到事件被锚点淘汰；
// 无时间戳（startedMS=0）的事件不参与锚点比较。
func TestTurnStateReduceStaleAnchor(t *testing.T) {
	prev := State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 5}
	t.Run("older tool_start discarded", func(t *testing.T) {
		got, err := Reduce(prev, EvToolStart, 1, 900, 6)
		if err != nil || got != prev {
			t.Fatalf("got %+v, err %v; want unchanged state, nil error", got, err)
		}
	})
	t.Run("older delta discarded", func(t *testing.T) {
		got, err := Reduce(prev, EvTextDelta, 1, 999, 6)
		if err != nil || got != prev {
			t.Fatalf("got %+v, err %v; want unchanged state, nil error", got, err)
		}
	})
	t.Run("untimestamped event accepted", func(t *testing.T) {
		got, err := Reduce(prev, EvTextDelta, 1, 0, 6)
		want := State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 6}
		if err != nil || got != want {
			t.Fatalf("got %+v, err %v; want %+v, nil error", got, err, want)
		}
	})
}

// TestTurnStateReduceTurnEndIsMultiTurnBoundary 检查 turn_end 只回到空闲边界，
// 绝不进入 finished；下一轮 turn_start 更新锚点后，上一轮的迟到事件被淘汰。
func TestTurnStateReduceTurnEndIsMultiTurnBoundary(t *testing.T) {
	steps := []struct {
		kind      string
		startedMS uint64
		seq       uint64
		want      State
	}{
		{EvTurnStart, 1000, 1, State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 1}},
		{EvTurnEnd, 1500, 2, State{Phase: PhaseIdle, AnchorMS: 1000, Seq: 2}},
		{EvTurnStart, 2000, 3, State{Phase: PhaseStreaming, AnchorMS: 2000, Seq: 3}},
		// 上一轮的 tool_end：时间戳 1600 早于新锚点 2000，被淘汰。
		{EvToolEnd, 1600, 4, State{Phase: PhaseStreaming, AnchorMS: 2000, Seq: 3}},
		{EvStop, 0, 5, State{Phase: PhaseFinished, AnchorMS: 2000, Seq: 5}},
	}
	state := State{}
	for i, step := range steps {
		got, err := Reduce(state, step.kind, i/2+1, step.startedMS, step.seq)
		if err != nil {
			t.Fatalf("step %d (%s): unexpected error: %v", i, step.kind, err)
		}
		if got != step.want {
			t.Fatalf("step %d (%s) = %+v, want %+v", i, step.kind, got, step.want)
		}
		state = got
	}
	if state.Phase != PhaseFinished {
		t.Fatalf("final phase = %v, want finished", state.Phase)
	}
}

// TestTurnStateReduceTerminalMustDeliver 检查终止事件的必达语义：只要 seq 更新，
// 即使没有时间戳或时间戳早于锚点，也一定推进到 finished。
func TestTurnStateReduceTerminalMustDeliver(t *testing.T) {
	cases := []struct {
		name      string
		prev      State
		startedMS uint64
	}{
		{"no timestamp", State{Phase: PhaseStreaming, AnchorMS: 5000, Seq: 10}, 0},
		{"older than anchor", State{Phase: PhaseAwaitingApproval, AnchorMS: 5000, Seq: 10}, 100},
		{"from idle", State{Seq: 10}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Reduce(tc.prev, EvStop, 1, tc.startedMS, tc.prev.Seq+1)
			want := State{Phase: PhaseFinished, AnchorMS: tc.prev.AnchorMS, Seq: tc.prev.Seq + 1}
			if err != nil || got != want {
				t.Fatalf("got %+v, err %v; want %+v, nil error", got, err, want)
			}
		})
	}

	// 同样的旧时间戳换成非终止事件则会被锚点淘汰，对比凸显必达语义。
	prev := State{Phase: PhaseStreaming, AnchorMS: 5000, Seq: 10}
	got, err := Reduce(prev, EvToolStart, 1, 100, 11)
	if err != nil || got != prev {
		t.Fatalf("non-terminal stale event got %+v, err %v; want unchanged", got, err)
	}
}

// TestTurnStateJSONOmitempty 检查所有字段的 omitempty 字节稳定性：
// 零值（含 idle）编码为 {}，非零字段按固定顺序输出且重复编码一致。
func TestTurnStateJSONOmitempty(t *testing.T) {
	cases := []struct {
		name  string
		state State
		want  string
	}{
		{"zero", State{}, `{}`},
		{"idle phase omitted", State{Phase: PhaseIdle}, `{}`},
		{"phase only", State{Phase: PhaseStreaming}, `{"phase":1}`},
		{"finished phase", State{Phase: PhaseFinished}, `{"phase":4}`},
		{"anchor only", State{AnchorMS: 1000}, `{"anchor_ms":1000}`},
		{"seq only", State{Seq: 7}, `{"seq":7}`},
		{"all fields", State{Phase: PhaseFinished, AnchorMS: 1000, Seq: 7}, `{"phase":4,"anchor_ms":1000,"seq":7}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.state)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Fatalf("json = %s, want %s", b, tc.want)
			}
			b2, err := json.Marshal(tc.state)
			if err != nil || string(b2) != string(b) {
				t.Fatalf("json not byte-stable: %s vs %s (err %v)", b, b2, err)
			}
		})
	}
}

// TestTurnStateStoreApplyAndLoad 检查 Store 的零值可用、陈旧事件不写、错误不改状态。
func TestTurnStateStoreApplyAndLoad(t *testing.T) {
	var s Store
	if got := s.Load(); got != (State{}) {
		t.Fatalf("zero Store.Load() = %+v, want zero State", got)
	}
	got, err := s.Apply(EvTurnStart, 1, 1000, 1)
	want := State{Phase: PhaseStreaming, AnchorMS: 1000, Seq: 1}
	if err != nil || got != want || s.Load() != want {
		t.Fatalf("after turn_start got %+v, store %+v, err %v; want %+v", got, s.Load(), err, want)
	}
	// 陈旧事件不改写状态。
	if got, err := s.Apply(EvTextDelta, 1, 0, 1); err != nil || got != want || s.Load() != want {
		t.Fatalf("stale apply got %+v, store %+v, err %v", got, s.Load(), err)
	}
	// 终结后非法事件返回哨兵错误且状态不变。
	if _, err := s.Apply(EvStop, 1, 0, 2); err != nil {
		t.Fatalf("stop: %v", err)
	}
	finished := s.Load()
	if _, err := s.Apply(EvTextDelta, 1, 0, 3); !errors.Is(err, ErrIllegalTransition) || s.Load() != finished {
		t.Fatalf("post-finish apply err = %v, store = %+v; want ErrIllegalTransition and unchanged", err, s.Load())
	}
}

// TestTurnStateStoreConcurrentApplyLoad 在并发 Apply（各自唯一递增 seq）与并发 Load
// 下验证 CAS 不丢更新：最终 seq 等于最大 seq，且单个读者看到的 seq 单调不减。
// 该测试配合 -race 检查数据竞争。
func TestTurnStateStoreConcurrentApplyLoad(t *testing.T) {
	const n = 200
	var s Store
	var wg sync.WaitGroup

	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			if _, err := s.Apply(EvTextDelta, 1, 0, seq); err != nil {
				t.Errorf("Apply(seq=%d): %v", seq, err)
			}
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
