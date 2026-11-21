package tui

import (
	"errors"
	"testing"
	"time"
)

// assertTurnState 比较权威回合状态的三个字段。锚点用 Equal 比较，
// 避免 time.Time 内部单调时钟/位置指针带来的误判。
func assertTurnState(t *testing.T, got, want TurnState) {
	t.Helper()
	if got.Phase != want.Phase || got.Seq != want.Seq || !got.AnchorTs.Equal(want.AnchorTs) {
		t.Fatalf("state = %+v, want %+v", got, want)
	}
}

// TestTurnPhaseString 检查四个阶段的稳定名称与未知阶段的回退值。
func TestTurnPhaseString(t *testing.T) {
	tests := []struct {
		phase TurnPhase
		want  string
	}{
		{TurnIdle, "idle"},
		{TurnThinking, "thinking"},
		{TurnToolRunning, "tool-running"},
		{TurnAnswering, "answering"},
		{TurnPhase(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.phase.String(); got != tt.want {
			t.Errorf("phase %d.String() = %q, want %q", tt.phase, got, tt.want)
		}
	}
}

// TestCanTransition 穷举四个阶段之间的全部 16 种组合，锁定合法与
// 非法转换矩阵；另外检查未知阶段值一律不被接受。
func TestCanTransition(t *testing.T) {
	phases := []TurnPhase{TurnIdle, TurnThinking, TurnToolRunning, TurnAnswering}
	want := map[[2]TurnPhase]bool{
		{TurnIdle, TurnIdle}:               false,
		{TurnIdle, TurnThinking}:           true,
		{TurnIdle, TurnToolRunning}:        false,
		{TurnIdle, TurnAnswering}:          false,
		{TurnThinking, TurnIdle}:           false,
		{TurnThinking, TurnThinking}:       false,
		{TurnThinking, TurnToolRunning}:    true,
		{TurnThinking, TurnAnswering}:      true,
		{TurnToolRunning, TurnIdle}:        false,
		{TurnToolRunning, TurnThinking}:    true,
		{TurnToolRunning, TurnToolRunning}: true,
		{TurnToolRunning, TurnAnswering}:   true,
		{TurnAnswering, TurnIdle}:          true,
		{TurnAnswering, TurnThinking}:      false,
		{TurnAnswering, TurnToolRunning}:   false,
		{TurnAnswering, TurnAnswering}:     true,
	}
	for _, from := range phases {
		for _, to := range phases {
			key := [2]TurnPhase{from, to}
			if got := CanTransition(from, to); got != want[key] {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want[key])
			}
		}
	}
	if CanTransition(TurnPhase(99), TurnThinking) {
		t.Error("unknown source phase must not allow a transition")
	}
	if CanTransition(TurnIdle, TurnPhase(99)) {
		t.Error("unknown target phase must not allow a transition")
	}
}

// TestTurnStateZeroValueIsIdle 检查零值是可直接使用的初始 idle 状态，
// 且从它开启第一个回合时锚点/序号被正确填充。
func TestTurnStateZeroValueIsIdle(t *testing.T) {
	var s TurnState
	if s.Phase != TurnIdle || s.Seq != 0 || !s.AnchorTs.IsZero() {
		t.Fatalf("zero state = %+v, want idle/zero-anchor/seq-0", s)
	}

	base := time.Unix(1_000, 0)
	next, err := s.Apply(TurnEvent{Phase: TurnThinking, At: base, Seq: 1})
	if err != nil {
		t.Fatalf("first transition failed: %v", err)
	}
	assertTurnState(t, next, TurnState{Phase: TurnThinking, AnchorTs: base, Seq: 1})
	assertTurnState(t, s, TurnState{}) // Apply 是纯函数，不修改接收者。
}

// TestTurnStateApplyLegalTransitions 用表驱动覆盖所有合法转换，
// 检查新状态的锚点/序号严格取自事件，且原状态不变。
func TestTurnStateApplyLegalTransitions(t *testing.T) {
	base := time.Unix(2_000, 0)
	t1, t2, t3 := base, base.Add(time.Second), base.Add(2*time.Second)

	tests := []struct {
		name  string
		start TurnState
		event TurnEvent
	}{
		{"idle to thinking", TurnState{}, TurnEvent{Phase: TurnThinking, At: t1, Seq: 1}},
		{"thinking to tool", TurnState{TurnThinking, t1, 1}, TurnEvent{Phase: TurnToolRunning, At: t2, Seq: 2}},
		{"thinking to answering", TurnState{TurnThinking, t1, 1}, TurnEvent{Phase: TurnAnswering, At: t2, Seq: 2}},
		{"tool to thinking", TurnState{TurnToolRunning, t2, 2}, TurnEvent{Phase: TurnThinking, At: t3, Seq: 3}},
		{"tool to answering", TurnState{TurnToolRunning, t2, 2}, TurnEvent{Phase: TurnAnswering, At: t3, Seq: 3}},
		{"tool re-anchor", TurnState{TurnToolRunning, t2, 2}, TurnEvent{Phase: TurnToolRunning, At: t3, Seq: 3}},
		{"answering to idle", TurnState{TurnAnswering, t3, 3}, TurnEvent{Phase: TurnIdle, At: t3.Add(time.Second), Seq: 4}},
		{"answering re-anchor", TurnState{TurnAnswering, t3, 3}, TurnEvent{Phase: TurnAnswering, At: t3.Add(time.Second), Seq: 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.start
			got, err := tt.start.Apply(tt.event)
			if err != nil {
				t.Fatalf("Apply(%s) failed: %v", tt.event.Phase, err)
			}
			assertTurnState(t, got, TurnState{Phase: tt.event.Phase, AnchorTs: tt.event.At, Seq: tt.event.Seq})
			assertTurnState(t, tt.start, before)
		})
	}
}

// TestTurnStateApplyRejectsStaleSeq 检查序号必须严格递增；重复、
// 倒退以及零值状态下的 seq=0 都被拒绝，且原状态不变。
func TestTurnStateApplyRejectsStaleSeq(t *testing.T) {
	base := time.Unix(3_000, 0)
	tests := []struct {
		name  string
		start TurnState
		event TurnEvent
	}{
		{"zero state seq zero", TurnState{}, TurnEvent{Phase: TurnThinking, At: base, Seq: 0}},
		{"equal seq", TurnState{TurnThinking, base, 5}, TurnEvent{Phase: TurnToolRunning, At: base.Add(time.Second), Seq: 5}},
		{"lower seq", TurnState{TurnThinking, base, 5}, TurnEvent{Phase: TurnAnswering, At: base.Add(time.Second), Seq: 4}},
		// 同时违反序号和阶段合法性时，固定先报告 seq，便于稳定诊断。
		{"seq outranks phase", TurnState{TurnIdle, base, 5}, TurnEvent{Phase: TurnAnswering, At: base, Seq: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.start
			got, err := tt.start.Apply(tt.event)
			if !errors.Is(err, ErrStaleTurnSeq) {
				t.Fatalf("Apply error = %v, want ErrStaleTurnSeq", err)
			}
			assertTurnState(t, got, before)
			assertTurnState(t, tt.start, before)
		})
	}
}

// TestTurnStateApplyRejectsStaleAnchor 检查早于当前锚点的事件一律
// 被拒绝——即使它的序号更新、目标阶段本来合法，也视为上一回合的
// 陈旧事件。
func TestTurnStateApplyRejectsStaleAnchor(t *testing.T) {
	base := time.Unix(4_000, 0)
	tests := []struct {
		name  string
		start TurnState
		event TurnEvent
	}{
		{"older than anchor", TurnState{TurnThinking, base, 5}, TurnEvent{Phase: TurnToolRunning, At: base.Add(-time.Second), Seq: 6}},
		{"legal phase but stale", TurnState{TurnToolRunning, base, 5}, TurnEvent{Phase: TurnThinking, At: base.Add(-time.Millisecond), Seq: 6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.start
			got, err := tt.start.Apply(tt.event)
			if !errors.Is(err, ErrStaleTurnAnchor) {
				t.Fatalf("Apply error = %v, want ErrStaleTurnAnchor", err)
			}
			assertTurnState(t, got, before)
			assertTurnState(t, tt.start, before)
		})
	}
}

// TestTurnStateApplyRejectsIllegalTransition 用表驱动覆盖非法阶段
// 组合：事件本身是新鲜的，只因阶段不合法而被拒绝。
func TestTurnStateApplyRejectsIllegalTransition(t *testing.T) {
	base := time.Unix(5_000, 0)
	tests := []struct {
		name string
		from TurnPhase
		to   TurnPhase
	}{
		{"idle to idle", TurnIdle, TurnIdle},
		{"idle to tool", TurnIdle, TurnToolRunning},
		{"idle to answering", TurnIdle, TurnAnswering},
		{"thinking to thinking", TurnThinking, TurnThinking},
		{"thinking to idle", TurnThinking, TurnIdle},
		{"tool to idle", TurnToolRunning, TurnIdle},
		{"answering to thinking", TurnAnswering, TurnThinking},
		{"answering to tool", TurnAnswering, TurnToolRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := TurnState{Phase: tt.from, AnchorTs: base, Seq: 7}
			got, err := start.Apply(TurnEvent{Phase: tt.to, At: base.Add(time.Second), Seq: 8})
			if !errors.Is(err, ErrIllegalTurnTransition) {
				t.Fatalf("Apply(%s -> %s) error = %v, want ErrIllegalTurnTransition", tt.from, tt.to, err)
			}
			assertTurnState(t, got, start)
			assertTurnState(t, start, TurnState{Phase: tt.from, AnchorTs: base, Seq: 7})
		})
	}
}

// TestTurnStateAnchorSemantics 用一个完整的时序场景锁定锚点语义：
// 锚点取最近一次被接受事件的时刻；更旧时刻的事件被拒绝；与锚点
// 同一时刻但序号更大的事件仍被接受。
func TestTurnStateAnchorSemantics(t *testing.T) {
	t0 := time.Unix(6_000, 0)
	tBefore := t0.Add(-time.Second)
	tAfter := t0.Add(2 * time.Second)

	s := TurnState{}
	var err error

	// 接受：锚点被设为 t0。
	s, err = s.Apply(TurnEvent{Phase: TurnThinking, At: t0, Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertTurnState(t, s, TurnState{TurnThinking, t0, 1})

	// 拒绝：序号更大但时刻早于锚点（来自上一回合）。
	if _, err = s.Apply(TurnEvent{Phase: TurnToolRunning, At: tBefore, Seq: 2}); !errors.Is(err, ErrStaleTurnAnchor) {
		t.Fatalf("older event error = %v, want ErrStaleTurnAnchor", err)
	}

	// 接受：新工具阶段把锚点刷新到 tAfter。
	s, err = s.Apply(TurnEvent{Phase: TurnToolRunning, At: tAfter, Seq: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertTurnState(t, s, TurnState{TurnToolRunning, tAfter, 2})

	// 拒绝：阶段合法（tool → thinking），但时刻落在旧锚点之前。
	if _, err = s.Apply(TurnEvent{Phase: TurnThinking, At: t0, Seq: 3}); !errors.Is(err, ErrStaleTurnAnchor) {
		t.Fatalf("stale-but-legal event error = %v, want ErrStaleTurnAnchor", err)
	}

	// 拒绝：时刻等于锚点，但序号回退（重复事件）。
	if _, err = s.Apply(TurnEvent{Phase: TurnThinking, At: tAfter, Seq: 1}); !errors.Is(err, ErrStaleTurnSeq) {
		t.Fatalf("duplicate event error = %v, want ErrStaleTurnSeq", err)
	}

	// 接受：时刻与锚点相同，序号更大——视为同回合内的一次新推进。
	s, err = s.Apply(TurnEvent{Phase: TurnThinking, At: tAfter, Seq: 3})
	if err != nil {
		t.Fatal(err)
	}
	assertTurnState(t, s, TurnState{TurnThinking, tAfter, 3})
}

// TestTurnStateSeqMonotonic 检查连续合法推进时序号严格递增，并始终
// 等于所接受事件的序号；锚点始终等于最近一次事件的时刻。
func TestTurnStateSeqMonotonic(t *testing.T) {
	base := time.Unix(7_000, 0)
	events := []TurnEvent{
		{Phase: TurnThinking, At: base, Seq: 1},
		{Phase: TurnToolRunning, At: base.Add(time.Second), Seq: 2},
		{Phase: TurnToolRunning, At: base.Add(2 * time.Second), Seq: 3},
		{Phase: TurnThinking, At: base.Add(3 * time.Second), Seq: 4},
		{Phase: TurnAnswering, At: base.Add(4 * time.Second), Seq: 5},
		{Phase: TurnAnswering, At: base.Add(5 * time.Second), Seq: 6},
		{Phase: TurnIdle, At: base.Add(6 * time.Second), Seq: 7},
	}

	var s TurnState
	for i, e := range events {
		next, err := s.Apply(e)
		if err != nil {
			t.Fatalf("event %d (%s) failed: %v", i, e.Phase, err)
		}
		if next.Seq != e.Seq || (i > 0 && next.Seq <= s.Seq) {
			t.Fatalf("event %d seq = %d, previous = %d", i, next.Seq, s.Seq)
		}
		if !next.AnchorTs.Equal(e.At) {
			t.Fatalf("event %d anchor = %v, want %v", i, next.AnchorTs, e.At)
		}
		s = next
	}
	assertTurnState(t, s, TurnState{TurnIdle, base.Add(6 * time.Second), 7})
}

// TestTurnStateAbort 检查用户中止可从任意活动阶段回到 idle 并刷新
// 锚点/序号；对 idle 中止、陈旧序号或陈旧时刻的中止均被拒绝。
func TestTurnStateAbort(t *testing.T) {
	base := time.Unix(8_000, 0)
	tests := []struct {
		name    string
		start   TurnState
		at      time.Time
		seq     uint64
		wantErr error
	}{
		{"from thinking", TurnState{TurnThinking, base, 2}, base.Add(time.Second), 3, nil},
		{"from tool", TurnState{TurnToolRunning, base, 2}, base.Add(time.Second), 3, nil},
		{"from answering", TurnState{TurnAnswering, base, 2}, base.Add(time.Second), 3, nil},
		{"from idle", TurnState{TurnIdle, base, 2}, base.Add(time.Second), 3, ErrIllegalTurnTransition},
		{"stale seq", TurnState{TurnThinking, base, 5}, base.Add(time.Second), 5, ErrStaleTurnSeq},
		{"stale anchor", TurnState{TurnThinking, base, 5}, base.Add(-time.Second), 6, ErrStaleTurnAnchor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.start
			got, err := tt.start.Abort(tt.at, tt.seq)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Abort failed: %v", err)
				}
				assertTurnState(t, got, TurnState{TurnIdle, tt.at, tt.seq})
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Abort error = %v, want %v", err, tt.wantErr)
			} else {
				assertTurnState(t, got, before)
			}
			assertTurnState(t, tt.start, before)
		})
	}
}
