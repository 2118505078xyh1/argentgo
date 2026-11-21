package llm_test

import (
	"sync"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// defaultSubstMaxDepth 与实现中的默认上限保持一致（实现未导出该常量）。
const defaultSubstMaxDepth = 32

// TestSubstBudgetEnterExitPairing 验证一次成功 Enter 配一次 Exit 后深度归零，
// Calls 累计每次 Enter（含失败调用），且在限额内不会误报 exhausted。
func TestSubstBudgetEnterExitPairing(t *testing.T) {
	b := llm.NewSubstBudget(4)
	for i := 0; i < 4; i++ {
		if !b.Enter() {
			t.Fatalf("第 %d 次 Enter 应成功", i+1)
		}
	}
	if b.Exhausted() {
		t.Fatal("未超限时不应报告 Exhausted")
	}
	if got := b.Calls(); got != 4 {
		t.Errorf("Calls() = %d, want 4", got)
	}

	// 再进入一层应被拒绝（同时计入 calls 并置 exhausted）。
	if b.Enter() {
		t.Fatal("超过上限的 Enter 应返回 false")
	}
	if !b.Exhausted() {
		t.Error("超限后 Exhausted 应为 true")
	}
	if got := b.Calls(); got != 5 {
		t.Errorf("Calls() = %d, want 5（含被拒绝的那次）", got)
	}

	// 为四次成功 Enter 配对 Exit；失败的那次不应配对（按契约深度残留不影响）。
	for i := 0; i < 4; i++ {
		b.Exit()
	}
	if b.Exhausted() != true {
		t.Error("配对退出后 exhausted 应保持 sticky")
	}
}

// TestSubstBudgetExitNeverBelowZero 验证对空预算器重复 Exit 不会让深度为负，
// 后续 Enter 仍能正常计数。
func TestSubstBudgetExitNeverBelowZero(t *testing.T) {
	b := llm.NewSubstBudget(2)
	for i := 0; i < 5; i++ {
		b.Exit()
	}
	for i := 0; i < 2; i++ {
		if !b.Enter() {
			t.Fatalf("多次提前 Exit 后第 %d 次 Enter 仍应成功", i+1)
		}
	}
	if b.Exhausted() {
		t.Error("限额内不应 exhausted")
	}
}

// TestSubstBudgetMaxDepthBoundary 围绕上限取值验证：恰好 maxDepth 次 Enter
// 成功，第 maxDepth+1 次被拒绝并置 exhausted；不同上限均成立。
func TestSubstBudgetMaxDepthBoundary(t *testing.T) {
	for _, maxDepth := range []int{1, 2, 3, 8} {
		b := llm.NewSubstBudget(maxDepth)
		for i := 0; i < maxDepth; i++ {
			if !b.Enter() {
				t.Fatalf("maxDepth=%d: 第 %d 次 Enter 应成功", maxDepth, i+1)
			}
		}
		if b.Exhausted() {
			t.Fatalf("maxDepth=%d: 恰在上限处不应 exhausted", maxDepth)
		}
		if b.Enter() {
			t.Fatalf("maxDepth=%d: 第 %d 次 Enter 应被拒绝", maxDepth, maxDepth+1)
		}
		if !b.Exhausted() {
			t.Fatalf("maxDepth=%d: 超限后应 exhausted", maxDepth)
		}
		if got := b.Calls(); got != maxDepth+1 {
			t.Errorf("maxDepth=%d: Calls() = %d, want %d", maxDepth, got, maxDepth+1)
		}
	}
}

// TestSubstBudgetExhaustedSticky 验证 exhausted 一旦置位，即使大量退出后
// 仍保持 true，便于展开回收之后回溯诊断是否触顶。
func TestSubstBudgetExhaustedSticky(t *testing.T) {
	b := llm.NewSubstBudget(1)
	if !b.Enter() {
		t.Fatal("首次 Enter 应成功")
	}
	if b.Enter() {
		t.Fatal("第二次 Enter 应被拒绝")
	}
	for i := 0; i < 100; i++ {
		b.Exit()
	}
	if !b.Exhausted() {
		t.Error("exhausted 应保持 sticky")
	}
}

// TestSubstBudgetZeroValueAndDefault 覆盖 <=0 采用默认上限，以及零值
// SubstBudget 可直接使用的约定：默认上限 32 次成功，第 33 次被拒绝。
func TestSubstBudgetZeroValueAndDefault(t *testing.T) {
	cases := []struct {
		name string
		b    *llm.SubstBudget
	}{
		{name: "zero limit", b: llm.NewSubstBudget(0)},
		{name: "negative limit", b: llm.NewSubstBudget(-7)},
		{name: "zero value", b: &llm.SubstBudget{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < defaultSubstMaxDepth; i++ {
				if !tc.b.Enter() {
					t.Fatalf("默认上限内第 %d 次 Enter 应成功", i+1)
				}
			}
			if tc.b.Exhausted() {
				t.Error("默认上限内不应 exhausted")
			}
			if tc.b.Enter() {
				t.Fatal("超过默认上限的 Enter 应被拒绝")
			}
			if !tc.b.Exhausted() {
				t.Error("超过默认上限后应 exhausted")
			}
			if got := tc.b.Calls(); got != defaultSubstMaxDepth+1 {
				t.Errorf("Calls() = %d, want %d", got, defaultSubstMaxDepth+1)
			}
		})
	}
}

// TestSubstBudgetConcurrent 从多个 goroutine 交错调用 Enter/Exit 与观测接口。
// 第一段用足够大的上限验证并发记账精确（配合 -race 捕获数据竞争）；第二段用
// 极小上限让大量 Enter 并发失败，压测 exhausted 写入路径的线程安全。
func TestSubstBudgetConcurrent(t *testing.T) {
	t.Run("exact accounting under large limit", func(t *testing.T) {
		const (
			goroutines = 8
			iterations = 200
		)
		b := llm.NewSubstBudget(goroutines * iterations) // 上限足够大，不会触发拒绝

		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < iterations; i++ {
					if !b.Enter() {
						t.Error("大上限下 Enter 不应被拒绝")
						return
					}
					_ = b.Exhausted()
					_ = b.Calls()
					b.Exit()
				}
			}()
		}
		wg.Wait()

		if got := b.Calls(); got != goroutines*iterations {
			t.Errorf("Calls() = %d, want %d", got, goroutines*iterations)
		}
		if b.Exhausted() {
			t.Error("大上限下不应 exhausted")
		}
	})

	t.Run("concurrent rejections set exhausted", func(t *testing.T) {
		const (
			goroutines = 16
			iterations = 50
		)
		b := llm.NewSubstBudget(1)

		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < iterations; i++ {
					b.Enter() // 多数会被拒绝；不配对 Exit，刻意压测失败路径
					_ = b.Exhausted()
				}
			}()
		}
		wg.Wait()

		if !b.Exhausted() {
			t.Error("大量并发 Enter 后应 exhausted")
		}
		if got := b.Calls(); got != goroutines*iterations {
			t.Errorf("Calls() = %d, want %d", got, goroutines*iterations)
		}
	})
}

// TestSubstBudgetCJKIndependent 验证预算器是纯计数器，与展开内容的字符无关：
// 分别对 ASCII 与 CJK 内容执行完全相同的 Enter/Exit 序列，得到一致的
// exhausted / Calls 结果。深度与调用计数不应因内容是多字节 CJK 而有差异。
func TestSubstBudgetCJKIndependent(t *testing.T) {
	ascii := llm.NewSubstBudget(3)
	cjk := llm.NewSubstBudget(3)

	// 这些标签仅表示"正在展开的内容"，预算器接口不接收也不解释它们。
	labels := []string{"a", "b", "c", "命令", "替换", "展开", "一", "二", "三"}
	for i, label := range labels {
		wantOK := i < 3
		if gotA, gotB := ascii.Enter(), cjk.Enter(); gotA != wantOK || gotB != wantOK {
			t.Fatalf("标签 %q: ascii.Enter=%v cjk.Enter=%v, want %v", label, gotA, gotB, wantOK)
		}
		_ = label
	}
	if ascii.Exhausted() != cjk.Exhausted() || !cjk.Exhausted() {
		t.Error("ASCII 与 CJK 内容的 exhausted 结果应一致且为 true")
	}
	if ascii.Calls() != cjk.Calls() {
		t.Errorf("ASCII Calls=%d, CJK Calls=%d, 应一致", ascii.Calls(), cjk.Calls())
	}
}
