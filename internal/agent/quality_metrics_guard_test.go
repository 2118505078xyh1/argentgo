package agent

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// 本文件为 R39 交付的三个并发质量测量原语提供统一的守卫测试：
//   - 并发守卫：多 goroutine 同时写同一个键、混合快照读取，断言计数精确、
//     无 panic、且每次快照都是副本（并发中被改动的快照不得污染内部状态）。
//   - 确定性守卫：相同操作序列在不同交错下产出一致的快照。
//   - CJK / 特殊输入安全：空串、纯空白、CJK、emoji 与超长文本不 panic 且语义正确。
//
// 与现有的单原语测试互补：这里的并发用例刻意让所有 goroutine 争用同一个键，
// 以捕捉"各写各键"覆盖不到的丢失更新。

// qmRecover 把 goroutine 内的 panic 转为可读的错误消息，便于"无 panic"守卫给出清晰诊断。
func qmRecover(errCh chan<- string) {
	if r := recover(); r != nil {
		errCh <- fmt.Sprintf("goroutine panic: %v", r)
	}
}

// qmFallbackStat 是 FallbackStats.Snapshot 结果结构在测试中的别名。
type qmFallbackStat struct {
	Attempts  int
	Successes int
	Rate      float64
}

// qmDrainErrors 汇总并发 goroutine 报告的错误（含被 qmRecover 捕获的 panic）。
func qmDrainErrors(t *testing.T, errCh chan string) {
	t.Helper()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}

// TestQualityMetricsConcurrentGuard 对三个原语做统一并发守卫。
func TestQualityMetricsConcurrentGuard(t *testing.T) {
	const workers = 16
	const iterations = 200

	t.Run("RecoveryMetrics", func(t *testing.T) {
		var m RecoveryMetrics
		errCh := make(chan string, workers)
		var wg sync.WaitGroup

		for id := 0; id < workers; id++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				defer qmRecover(errCh)
				for j := 0; j < iterations; j++ {
					// 所有 goroutine 争用同一个键：三种写操作各占一组。
					switch id % 3 {
					case 0:
						m.RecordAttempt("shared")
					case 1:
						m.RecordSuccess("shared")
					case 2:
						m.RecordEscalation("shared")
					}
					// 混合读：拿到快照后写入哨兵键，验证副本语义且不污染内部状态。
					snap := m.Snapshot()
					snap[fmt.Sprintf("leak-%d", id)] = RecoveryStats{Attempts: 999}
				}
			}(id)
		}
		wg.Wait()
		qmDrainErrors(t, errCh)

		// 三种写操作的次数由分组确定，与交错顺序无关。
		attempts, successes := 0, 0
		for id := 0; id < workers; id++ {
			switch id % 3 {
			case 0:
				attempts += iterations
			case 1:
				successes += iterations
			}
		}
		snap := m.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("Snapshot() 键数 = %d, want 1（哨兵键不得泄漏）：%#v", len(snap), snap)
		}
		got := snap["shared"]
		if got.Attempts != attempts || got.Successes != successes {
			t.Fatalf("shared = %+v, want attempts=%d successes=%d", got, attempts, successes)
		}
		if wantRate := float64(successes) / float64(attempts); got.Rate != wantRate {
			t.Fatalf("Rate = %v, want %v", got.Rate, wantRate)
		}
	})

	t.Run("FallbackStats", func(t *testing.T) {
		var s FallbackStats
		errCh := make(chan string, workers)
		var wg sync.WaitGroup

		for id := 0; id < workers; id++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				defer qmRecover(errCh)
				for j := 0; j < iterations; j++ {
					// 所有 goroutine 争用同一个原因，奇偶决定成败。
					s.Record("shared", id%2 == 0)
					// 混合读并改动副本：哨兵原因不得污染内部状态。
					snap := s.Snapshot()
					snap[FallbackReason(fmt.Sprintf("leak-%d", id))] = struct {
						Attempts  int
						Successes int
						Rate      float64
					}{Attempts: 999}
				}
			}(id)
		}
		wg.Wait()
		qmDrainErrors(t, errCh)

		snap := s.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("Snapshot() 键数 = %d, want 1（哨兵原因不得泄漏）：%#v", len(snap), snap)
		}
		got := qmFallbackStat(snap["shared"])
		wantAttempts := workers * iterations
		wantSuccesses := (workers / 2) * iterations
		if got.Attempts != wantAttempts || got.Successes != wantSuccesses || got.Rate != 0.5 {
			t.Fatalf("shared = %+v, want attempts=%d successes=%d rate=0.5",
				got, wantAttempts, wantSuccesses)
		}
	})

	t.Run("ErrorPatternTracker", func(t *testing.T) {
		tracker := NewErrorPatternTracker(0, 0)
		errCh := make(chan string, workers)
		var wg sync.WaitGroup

		for id := 0; id < workers; id++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				defer qmRecover(errCh)
				for j := 0; j < iterations; j++ {
					// 仅易变数字不同，全部折叠到同一签名。
					tracker.Record(fmt.Sprintf("连接错误：重试 %d 次", j+1))
					// 混合读并改动 Top 返回的切片，不得污染内部状态。
					top := tracker.Top()
					if len(top) > 0 {
						top[0].Count = -1
						top[0].Signature = "corrupted"
					}
				}
			}(id)
		}
		wg.Wait()
		qmDrainErrors(t, errCh)

		top := tracker.Top()
		if len(top) != 1 {
			t.Fatalf("Top() 条目数 = %d, want 1：%#v", len(top), top)
		}
		if want := workers * iterations; top[0].Count != want {
			t.Fatalf("并发计数 = %d, want %d", top[0].Count, want)
		}
		if top[0].Signature != "连接错误：重试 <*> 次" {
			t.Fatalf("并发中被改动的 Top 污染了内部签名：%q", top[0].Signature)
		}
	})
}

// TestQualityMetricsDeterminismGuard 验证相同操作序列在不同交错下产出一致的快照。
func TestQualityMetricsDeterminismGuard(t *testing.T) {
	t.Run("RecoveryMetrics", func(t *testing.T) {
		// 每种失败的操作序列固定，仅跨种类的全局交错顺序不同。
		ops := map[string]string{
			"mistake":     "aass",
			"stuck":       "aaa",
			"no_progress": "asae",
		}
		kinds := []string{"mistake", "stuck", "no_progress"}
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

		var grouped RecoveryMetrics
		for _, kind := range kinds {
			apply(&grouped, kind, ops[kind])
		}

		var interleaved RecoveryMetrics
		// 逆序轮转，制造与 grouped 完全不同的首次出现/写入顺序。
		for step := 0; step < 4; step++ {
			for i := len(kinds) - 1; i >= 0; i-- {
				kind := kinds[i]
				if step < len(ops[kind]) {
					apply(&interleaved, kind, ops[kind][step:step+1])
				}
			}
		}

		want := grouped.Snapshot()
		if got := interleaved.Snapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("交错 Snapshot() = %+v, want %+v", got, want)
		}
		for i := 0; i < 10; i++ {
			if got := grouped.Snapshot(); !reflect.DeepEqual(got, want) {
				t.Fatalf("第 %d 次 Snapshot() 漂移 = %+v, want %+v", i, got, want)
			}
		}
	})

	t.Run("FallbackStats", func(t *testing.T) {
		ops := map[FallbackReason]string{
			"status":  "ssf",
			"timeout": "sff",
			"限流":      "sss",
		}
		reasons := []FallbackReason{"status", "timeout", "限流"}
		apply := func(s *FallbackStats, reason FallbackReason, seq string) {
			for _, op := range seq {
				s.Record(reason, op == 's')
			}
		}

		var grouped FallbackStats
		for _, reason := range reasons {
			apply(&grouped, reason, ops[reason])
		}

		var interleaved FallbackStats
		for step := 0; step < 3; step++ {
			for i := len(reasons) - 1; i >= 0; i-- {
				reason := reasons[i]
				if step < len(ops[reason]) {
					apply(&interleaved, reason, ops[reason][step:step+1])
				}
			}
		}

		want := grouped.Snapshot()
		if got := interleaved.Snapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("交错 Snapshot() = %+v, want %+v", got, want)
		}
	})

	t.Run("ErrorPatternTracker", func(t *testing.T) {
		// 三个签名的计数刻意互不相同（3/2/1），使 Top 顺序只由计数决定，
		// 与首次出现顺序无关，从而任何交错都产出完全一致的 Top。
		groups := [][]string{
			{"alpha 1", "alpha 2", "alpha 3"},
			{"beta 1", "beta 2"},
			{"gamma 1"},
		}
		record := func(tr *ErrorPatternTracker, err string) { tr.Record(err) }

		grouped := NewErrorPatternTracker(0, 0)
		for _, group := range groups {
			for _, err := range group {
				record(grouped, err)
			}
		}

		interleaved := NewErrorPatternTracker(0, 0)
		for step := 0; step < 3; step++ {
			for i := len(groups) - 1; i >= 0; i-- {
				if step < len(groups[i]) {
					record(interleaved, groups[i][step])
				}
			}
		}

		want := grouped.Top()
		if got := interleaved.Top(); !reflect.DeepEqual(got, want) {
			t.Fatalf("交错 Top() = %#v, want %#v", got, want)
		}
		// 期望顺序由计数唯一确定：alpha(3), beta(2), gamma(1)。
		wantOrdered := []ErrorPattern{
			{Signature: "alpha <*>", Count: 3},
			{Signature: "beta <*>", Count: 2},
			{Signature: "gamma <*>", Count: 1},
		}
		if !reflect.DeepEqual(want, wantOrdered) {
			t.Fatalf("grouped Top() = %#v, want %#v", want, wantOrdered)
		}
	})
}

// TestQualityMetricsCJKAndSpecialInputGuard 验证 CJK 与特殊输入安全：不 panic、语义正确、签名 UTF-8 合法。
func TestQualityMetricsCJKAndSpecialInputGuard(t *testing.T) {
	t.Run("RecoveryMetrics", func(t *testing.T) {
		var m RecoveryMetrics
		m.RecordAttempt("错误：连接超时")
		m.RecordSuccess("错误：连接超时")
		m.RecordAttempt("🛠️ 重试")
		m.RecordEscalation("") // 空串键
		m.RecordAttempt("   ") // 纯空白键

		want := map[string]RecoveryStats{
			"错误：连接超时": {Attempts: 1, Successes: 1, Rate: 1, Outcome: RecoveryOutcomeRecovered},
			"🛠️ 重试":   {Attempts: 1, Successes: 0, Rate: 0, Outcome: RecoveryOutcomePersisted},
			"":        {Attempts: 0, Successes: 0, Rate: 0, Outcome: RecoveryOutcomeEscalated},
			"   ":     {Attempts: 1, Successes: 0, Rate: 0, Outcome: RecoveryOutcomePersisted},
		}
		if got := m.Snapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Snapshot() = %+v, want %+v", got, want)
		}
	})

	t.Run("FallbackStats", func(t *testing.T) {
		var s FallbackStats
		s.Record("限流", true)
		s.Record("限流", false)
		s.Record("网络超时 🛠️", true)
		s.Record("", false) // 空串原因

		want := map[FallbackReason]qmFallbackStat{
			"限流":      {Attempts: 2, Successes: 1, Rate: 0.5},
			"网络超时 🛠️": {Attempts: 1, Successes: 1, Rate: 1},
			"":        {Attempts: 1, Successes: 0, Rate: 0},
		}
		got := map[FallbackReason]qmFallbackStat{}
		for reason, v := range s.Snapshot() {
			got[reason] = qmFallbackStat(v)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Snapshot() = %+v, want %+v", got, want)
		}
	})

	t.Run("ErrorPatternTracker", func(t *testing.T) {
		tracker := NewErrorPatternTracker(0, 0)
		tracker.Record("连接超时：重试 3 次")
		tracker.Record("连接超时：重试 9 次") // 仅数字不同 -> 折叠到同一签名，计数 2
		tracker.Record("权限不足 🛠️")
		tracker.Record("") // 空错误 -> 空签名
		// 超长 CJK：按 rune 截断，绝不能切出半个多字节字符。
		tracker.Record("错误" + strings.Repeat("字", maxSignatureRunes+50))

		top := tracker.Top()
		if len(top) != 4 {
			t.Fatalf("Top() 条目数 = %d, want 4：%#v", len(top), top)
		}
		for _, p := range top {
			if !utf8.ValidString(p.Signature) {
				t.Fatalf("签名出现非法 UTF-8：%q", p.Signature)
			}
		}
		found := false
		for _, p := range top {
			if p.Signature == "连接超时：重试 <*> 次" {
				found = true
				if p.Count != 2 {
					t.Fatalf("折叠签名计数 = %d, want 2", p.Count)
				}
			}
		}
		if !found {
			t.Fatalf("未找到折叠后的 CJK 签名：%#v", top)
		}

		// 截断签名应是 maxSignatureRunes 个 rune。
		truncated := false
		for _, p := range top {
			if strings.HasPrefix(p.Signature, "错误") && len([]rune(p.Signature)) == maxSignatureRunes {
				truncated = true
			}
		}
		if !truncated {
			t.Fatalf("未找到按 rune 截断的超长 CJK 签名：%#v", top)
		}
	})
}
