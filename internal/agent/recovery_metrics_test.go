package agent

import (
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
)

func TestRecoveryMetricsSuccessRate(t *testing.T) {
	// 恢复成功率 = 成功数 / 尝试数。
	var m RecoveryMetrics
	for i := 0; i < 4; i++ {
		m.RecordAttempt("api_error")
	}
	m.RecordSuccess("api_error")
	m.RecordSuccess("api_error")

	got := m.Snapshot()["api_error"]
	want := RecoveryStats{
		Attempts:  4,
		Successes: 2,
		Rate:      0.5,
		Outcome:   RecoveryOutcomeRecovered,
	}
	if got != want {
		t.Fatalf("Snapshot()[api_error] = %+v, want %+v", got, want)
	}
}

func TestRecoveryMetricsRateClamped(t *testing.T) {
	// 调用方若只记录成功而不记录尝试，Rate 也必须被钳制在 [0,1] 内。
	var m RecoveryMetrics
	m.RecordAttempt("kind")
	for i := 0; i < 10; i++ {
		m.RecordSuccess("kind")
	}
	got := m.Snapshot()["kind"]
	if got.Rate != 1 {
		t.Fatalf("Rate = %v, want clamped 1", got.Rate)
	}
	if math.IsNaN(got.Rate) || math.IsInf(got.Rate, 0) {
		t.Fatalf("Rate is not finite: %v", got.Rate)
	}
}

func TestRecoveryMetricsKindsIsolated(t *testing.T) {
	// 不同失败种类各自独立累计，改写一种不影响其它种类。
	var m RecoveryMetrics
	m.RecordAttempt("mistake")
	m.RecordSuccess("mistake")
	for i := 0; i < 3; i++ {
		m.RecordAttempt("stuck")
	}
	m.RecordAttempt("no_progress")
	m.RecordSuccess("no_progress")
	m.RecordEscalation("no_progress")

	snap := m.Snapshot()
	want := map[string]RecoveryStats{
		"mistake":     {Attempts: 1, Successes: 1, Rate: 1, Outcome: RecoveryOutcomeRecovered},
		"stuck":       {Attempts: 3, Successes: 0, Rate: 0, Outcome: RecoveryOutcomePersisted},
		"no_progress": {Attempts: 1, Successes: 1, Rate: 1, Outcome: RecoveryOutcomeEscalated},
	}
	if !reflect.DeepEqual(snap, want) {
		t.Fatalf("Snapshot() = %+v, want %+v", snap, want)
	}

	// 继续写 "stuck" 不应改变另外两种。
	m.RecordSuccess("stuck")
	snap = m.Snapshot()
	if got := snap["stuck"]; got.Attempts != 3 || got.Successes != 1 || got.Rate != 1.0/3.0 {
		t.Fatalf("stuck stats = %+v, want 3 attempts/1 success", got)
	}
	if got := snap["mistake"]; got != want["mistake"] {
		t.Fatalf("mistake changed = %+v, want %+v", got, want["mistake"])
	}
	if got := snap["no_progress"]; got != want["no_progress"] {
		t.Fatalf("no_progress changed = %+v, want %+v", got, want["no_progress"])
	}
}

func TestRecoveryMetricsZeroAttemptsNoNaN(t *testing.T) {
	// 零值与 nil 接收者的快照都是非 nil 空 map。
	var m RecoveryMetrics
	if snap := m.Snapshot(); snap == nil || len(snap) != 0 {
		t.Fatalf("zero-value Snapshot() = %v, want non-nil empty map", snap)
	}
	var nilPtr *RecoveryMetrics
	if snap := nilPtr.Snapshot(); snap == nil || len(snap) != 0 {
		t.Fatalf("nil Snapshot() = %v, want non-nil empty map", snap)
	}

	// 只有成功、没有尝试：Attempts 为零，Rate 必须为零而非 NaN/Inf。
	m.RecordSuccess("only_success")
	m.RecordEscalation("only_escalation")
	snap := m.Snapshot()
	if got := snap["only_success"]; got.Attempts != 0 || got.Successes != 1 || got.Rate != 0 {
		t.Fatalf("only_success = %+v, want 0 attempts/1 success/rate 0", got)
	}
	if got := snap["only_escalation"]; got.Attempts != 0 || got.Successes != 0 || got.Rate != 0 {
		t.Fatalf("only_escalation = %+v, want zero counts and rate 0", got)
	}
	for kind, stats := range snap {
		if math.IsNaN(stats.Rate) || math.IsInf(stats.Rate, 0) || stats.Rate < 0 || stats.Rate > 1 {
			t.Fatalf("%q has invalid Rate %v", kind, stats.Rate)
		}
	}

	// nil 接收者的写方法同样安全。
	nilPtr.RecordAttempt("kind")
	nilPtr.RecordSuccess("kind")
	nilPtr.RecordEscalation("kind")
}

func TestRecoveryMetricsOutcomes(t *testing.T) {
	var m RecoveryMetrics
	m.RecordAttempt("kind")
	if got := m.Snapshot()["kind"].Outcome; got != RecoveryOutcomePersisted {
		t.Fatalf("Outcome after attempt = %q, want %q", got, RecoveryOutcomePersisted)
	}
	m.RecordSuccess("kind")
	if got := m.Snapshot()["kind"].Outcome; got != RecoveryOutcomeRecovered {
		t.Fatalf("Outcome after success = %q, want %q", got, RecoveryOutcomeRecovered)
	}
	m.RecordEscalation("kind")
	if got := m.Snapshot()["kind"].Outcome; got != RecoveryOutcomeEscalated {
		t.Fatalf("Outcome after escalation = %q, want %q", got, RecoveryOutcomeEscalated)
	}
	m.RecordAttempt("kind")
	if got := m.Snapshot()["kind"].Outcome; got != RecoveryOutcomePersisted {
		t.Fatalf("Outcome after new attempt = %q, want %q", got, RecoveryOutcomePersisted)
	}
}

func TestRecoveryMetricsSnapshotIsCopy(t *testing.T) {
	var m RecoveryMetrics
	m.RecordAttempt("a")
	m.RecordSuccess("a")
	m.RecordAttempt("b")

	snap := m.Snapshot()
	// 调用方修改快照不得影响内部状态。
	snap["a"] = RecoveryStats{}
	delete(snap, "b")
	snap["c"] = RecoveryStats{Attempts: 9}

	after := m.Snapshot()
	want := map[string]RecoveryStats{
		"a": {Attempts: 1, Successes: 1, Rate: 1, Outcome: RecoveryOutcomeRecovered},
		"b": {Attempts: 1, Successes: 0, Rate: 0, Outcome: RecoveryOutcomePersisted},
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("Snapshot() after mutation = %+v, want %+v", after, want)
	}
	if _, ok := after["c"]; ok {
		t.Fatalf("mutating snapshot leaked a new key into internal state")
	}

	// 两次快照彼此独立。
	first := m.Snapshot()
	second := m.Snapshot()
	first["a"] = RecoveryStats{}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("mutating first snapshot changed second: %+v", second)
	}
}

func TestRecoveryMetricsDeterministic(t *testing.T) {
	// 每种失败各自的操作序列固定，仅跨种类的交错顺序不同；
	// 快照必须逐字节一致，与写入的全局顺序无关。
	ops := map[string]string{
		"mistake":     "aass",
		"stuck":       "aaa",
		"no_progress": "asae",
	}
	apply := func(m *RecoveryMetrics, kind, seq string) {
		for _, op := range seq {
			switch op {
			case 'a':
				m.RecordAttempt(kind)
			case 's':
				m.RecordSuccess(kind)
			case 'e':
				m.RecordEscalation(kind)
			}
		}
	}

	var sequential RecoveryMetrics
	kinds := []string{"mistake", "stuck", "no_progress"}
	for _, kind := range kinds {
		apply(&sequential, kind, ops[kind])
	}

	var interleaved RecoveryMetrics
	for step := 0; step < 4; step++ {
		for _, kind := range kinds {
			if step < len(ops[kind]) {
				apply(&interleaved, kind, string(ops[kind][step]))
			}
		}
	}

	want := sequential.Snapshot()
	if got := interleaved.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("interleaved Snapshot() = %+v, want %+v", got, want)
	}
	// 反复读取同一状态，Rate 作为整数计数的确定性函数不应漂移。
	for i := 0; i < 10; i++ {
		if got := sequential.Snapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Snapshot() #%d = %+v, want %+v", i, got, want)
		}
	}
}

func TestRecoveryMetricsConcurrentAccess(t *testing.T) {
	// 每个 goroutine 只写自己的种类，同时让 Snapshot 与写入并发，
	// 由竞态检测器验证锁的正确性。
	var m RecoveryMetrics
	const workers = 32
	const iterations = 100
	errCh := make(chan string, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("kind-%d", id)
			for j := 0; j < iterations; j++ {
				m.RecordAttempt(key)
				m.RecordSuccess(key)
				_ = m.Snapshot()
			}
			m.RecordEscalation(key)

			got := m.Snapshot()[key]
			if got.Attempts != iterations || got.Successes != iterations ||
				got.Rate != 1 || got.Outcome != RecoveryOutcomeEscalated {
				errCh <- fmt.Sprintf("kind %q stats = %+v, want %d attempts/%d successes", key, got, iterations, iterations)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
	if snap := m.Snapshot(); len(snap) != workers {
		t.Fatalf("Snapshot() has %d kinds, want %d", len(snap), workers)
	}
}
