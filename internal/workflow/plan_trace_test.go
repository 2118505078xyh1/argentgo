package workflow

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// TestPlanTraceMarkAndSnapshot 表驱动验证 Mark 纯追加、Snapshot 精确保序，
// 以及重复条目不被合并、时间戳原样保留。
func TestPlanTraceMarkAndSnapshot(t *testing.T) {
	cases := []struct {
		name  string
		marks []TraceEntry
		want  []TraceEntry
	}{
		{
			name: "empty trace snapshots to nil",
		},
		{
			name:  "single mark",
			marks: []TraceEntry{{Step: "a", Phase: "start", At: 1}},
			want:  []TraceEntry{{Step: "a", Phase: "start", At: 1}},
		},
		{
			name: "multiple marks keep insertion order and all fields",
			marks: []TraceEntry{
				{Step: "a", Phase: "start", At: 10},
				{Step: "b", Phase: "start", At: 20},
				{Step: "a", Phase: "done", At: 30},
			},
			want: []TraceEntry{
				{Step: "a", Phase: "start", At: 10},
				{Step: "b", Phase: "start", At: 20},
				{Step: "a", Phase: "done", At: 30},
			},
		},
		{
			name: "identical entries are each appended",
			marks: []TraceEntry{
				{Step: "a", Phase: "ready", At: 0},
				{Step: "a", Phase: "ready", At: 0},
			},
			want: []TraceEntry{
				{Step: "a", Phase: "ready", At: 0},
				{Step: "a", Phase: "ready", At: 0},
			},
		},
		{
			name: "timestamps are kept verbatim including negative values",
			marks: []TraceEntry{
				{Step: "z", Phase: "p", At: -5},
				{Step: "z", Phase: "p", At: 100},
				{Step: "z", Phase: "p", At: 7},
			},
			want: []TraceEntry{
				{Step: "z", Phase: "p", At: -5},
				{Step: "z", Phase: "p", At: 100},
				{Step: "z", Phase: "p", At: 7},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewPlanTrace()
			for _, e := range tc.marks {
				tr.Mark(e.Step, e.Phase, e.At)
			}
			if got := tr.Snapshot(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Snapshot() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestPlanTraceStepsInPhase 表驱动验证按相位过滤、首次出现顺序去重与稳定性。
func TestPlanTraceStepsInPhase(t *testing.T) {
	marks := []TraceEntry{
		{Step: "a", Phase: "ready", At: 1},
		{Step: "b", Phase: "running", At: 2},
		{Step: "a", Phase: "running", At: 3},
		{Step: "c", Phase: "ready", At: 4},
		{Step: "b", Phase: "done", At: 5},
		{Step: "c", Phase: "ready", At: 6},
		{Step: "a", Phase: "ready", At: 7},
	}

	cases := []struct {
		phase string
		want  []string
	}{
		{phase: "ready", want: []string{"a", "c"}},
		{phase: "running", want: []string{"b", "a"}},
		{phase: "done", want: []string{"b"}},
		{phase: "missing", want: nil},
		{phase: "", want: nil},
	}

	tr := NewPlanTrace()
	for _, e := range marks {
		tr.Mark(e.Step, e.Phase, e.At)
	}

	for _, tc := range cases {
		t.Run("phase="+tc.phase, func(t *testing.T) {
			if got := tr.StepsInPhase(tc.phase); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("StepsInPhase(%q) = %#v, want %#v", tc.phase, got, tc.want)
			}
		})
	}
}

// TestPlanTraceCJK 覆盖中文步骤名与中文相位名：Snapshot 原样保留 Unicode，
// StepsInPhase 对同一步骤的重复中文相位按首次出现去重。
func TestPlanTraceCJK(t *testing.T) {
	tr := NewPlanTrace()
	tr.Mark("扫描", "就绪", 1)
	tr.Mark("复核", "运行中", 2)
	tr.Mark("扫描", "完成", 3)
	tr.Mark("扫描", "就绪", 4)
	tr.Mark("汇总", "就绪", 5)

	want := []TraceEntry{
		{Step: "扫描", Phase: "就绪", At: 1},
		{Step: "复核", Phase: "运行中", At: 2},
		{Step: "扫描", Phase: "完成", At: 3},
		{Step: "扫描", Phase: "就绪", At: 4},
		{Step: "汇总", Phase: "就绪", At: 5},
	}
	if got := tr.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, want)
	}
	if got := tr.StepsInPhase("就绪"); !reflect.DeepEqual(got, []string{"扫描", "汇总"}) {
		t.Fatalf("StepsInPhase(就绪) = %#v, want [扫描 汇总]", got)
	}
	if got := tr.StepsInPhase("运行中"); !reflect.DeepEqual(got, []string{"复核"}) {
		t.Fatalf("StepsInPhase(运行中) = %#v, want [复核]", got)
	}
}

// TestPlanTraceZeroValue 验证零值 PlanTrace 与 nil 接收者都可用：
// 零值可直接追加与查询，nil 接收者全部安全返回空。
func TestPlanTraceZeroValue(t *testing.T) {
	var tr PlanTrace
	if got := tr.Snapshot(); got != nil {
		t.Fatalf("zero value Snapshot() = %#v, want nil", got)
	}
	if got := tr.StepsInPhase("ready"); got != nil {
		t.Fatalf("zero value StepsInPhase() = %#v, want nil", got)
	}

	tr.Mark("a", "ready", 1)
	tr.Mark("b", "ready", 2)
	want := []TraceEntry{{Step: "a", Phase: "ready", At: 1}, {Step: "b", Phase: "ready", At: 2}}
	if got := tr.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("zero value after Mark: Snapshot() = %#v, want %#v", got, want)
	}
	if got := tr.StepsInPhase("ready"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("zero value after Mark: StepsInPhase() = %#v, want [a b]", got)
	}

	var nilTrace *PlanTrace
	nilTrace.Mark("a", "ready", 1)
	if got := nilTrace.Snapshot(); got != nil {
		t.Fatalf("nil receiver Snapshot() = %#v, want nil", got)
	}
	if got := nilTrace.StepsInPhase("ready"); got != nil {
		t.Fatalf("nil receiver StepsInPhase() = %#v, want nil", got)
	}
}

// TestPlanTraceCopiesAreIndependent 验证返回值与内部状态零共享：篡改 Snapshot
// 与 StepsInPhase 的结果都不会污染轨迹；旧快照也不会被后续 Mark 改写。
func TestPlanTraceCopiesAreIndependent(t *testing.T) {
	tr := NewPlanTrace()
	tr.Mark("a", "ready", 1)
	tr.Mark("b", "ready", 2)

	snap := tr.Snapshot()
	snap[0].Step = "mutated-step"
	snap[1].Phase = "mutated-phase"
	snap = append(snap, TraceEntry{Step: "injected", Phase: "ready", At: 99})

	steps := tr.StepsInPhase("ready")
	steps[0] = "mutated-name"

	wantEntries := []TraceEntry{{Step: "a", Phase: "ready", At: 1}, {Step: "b", Phase: "ready", At: 2}}
	if got := tr.Snapshot(); !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("mutating Snapshot result corrupted trace: %#v, want %#v", got, wantEntries)
	}
	if got := tr.StepsInPhase("ready"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("mutating StepsInPhase result corrupted trace: %#v, want [a b]", got)
	}

	// 已取得的快照不受后续 Mark 影响。
	before := tr.Snapshot()
	tr.Mark("c", "ready", 3)
	if !reflect.DeepEqual(before, wantEntries) {
		t.Fatalf("later Mark changed earlier snapshot: %#v, want %#v", before, wantEntries)
	}
	if got := tr.StepsInPhase("ready"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("StepsInPhase() after Mark = %#v, want [a b c]", got)
	}
}

// TestPlanTraceConcurrentMarkAndSnapshot 在多个 goroutine 中并发执行 Mark、
// Snapshot 与 StepsInPhase。配合 -race 检查锁保护是否正确，并断言最终条目数
// 精确等于全部 Mark 次数（不丢条目、不重复）。
func TestPlanTraceConcurrentMarkAndSnapshot(t *testing.T) {
	tr := NewPlanTrace()
	const (
		goroutines = 8
		perG       = 200
	)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				tr.Mark(fmt.Sprintf("step-%d-%d", id, i), "ready", int64(i))
				_ = tr.Snapshot()
				_ = tr.StepsInPhase("ready")
			}
		}(g)
	}
	wg.Wait()

	got := tr.Snapshot()
	wantCount := goroutines * perG
	if len(got) != wantCount {
		t.Fatalf("len(Snapshot()) = %d, want %d", len(got), wantCount)
	}
	seen := make(map[string]bool, wantCount)
	for _, e := range got {
		if e.Phase != "ready" || seen[e.Step] {
			t.Fatalf("unexpected or duplicate entry: %#v", e)
		}
		seen[e.Step] = true
	}
	if gotSteps := tr.StepsInPhase("ready"); len(gotSteps) != wantCount {
		t.Fatalf("len(StepsInPhase(ready)) = %d, want %d", len(gotSteps), wantCount)
	}
}
