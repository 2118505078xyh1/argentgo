package agent

import (
	"math/big"
	"strings"
	"testing"
)

// pressureMaxInt 返回当前平台的 math.MaxInt，避免为取一个常量而引入 math 包。
func pressureMaxInt() int {
	return int(^uint(0) >> 1)
}

func TestClassifyPressureBoundaries(t *testing.T) {
	// 以 100 为预算，阈值恰好落在整数上，锁定"上界不含"的精确边界。
	const budget = 100
	cases := []struct {
		name string
		used int
		want PressureLevel
	}{
		{"zero used", 0, levelLow},
		{"just below 50", 49, levelLow},
		{"exactly 50", 50, levelNormal},
		{"just below 70", 69, levelNormal},
		{"exactly 70", 70, levelElevated},
		{"just below 85", 84, levelElevated},
		{"exactly 85", 85, levelHigh},
		{"just below 95", 94, levelHigh},
		{"exactly 95", 95, levelCritical},
		{"full budget", 100, levelCritical},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPressure(tc.used, budget)
			if got != tc.want {
				t.Fatalf("ClassifyPressure(%d, %d) = %v (%s), want %v (%s)",
					tc.used, budget, got, PressureLabel(got), tc.want, PressureLabel(tc.want))
			}
		})
	}
}

func TestClassifyPressureAwkwardBudget(t *testing.T) {
	// 预算为 7 时阈值都落在分数上，验证整数交叉相乘不会把边界算偏。
	const budget = 7
	cases := []struct {
		used int
		want PressureLevel
	}{
		{3, levelLow},      // 3/7 ≈ 0.428 < 0.5
		{4, levelNormal},   // 4/7 ≈ 0.571，落在 50%~70%
		{5, levelElevated}, // 5/7 ≈ 0.714，落在 70%~85%
		{6, levelHigh},     // 6/7 ≈ 0.857，落在 85%~95%
		{7, levelCritical}, // 7/7 = 1
	}
	for _, tc := range cases {
		if got := ClassifyPressure(tc.used, budget); got != tc.want {
			t.Fatalf("ClassifyPressure(%d, %d) = %v, want %v", tc.used, budget, got, tc.want)
		}
	}
}

func TestClassifyPressureNoBudget(t *testing.T) {
	max := pressureMaxInt()
	cases := []struct {
		name         string
		used, budget int
	}{
		{"zero budget zero used", 0, 0},
		{"zero budget positive used", 1, 0},
		{"zero budget huge used", max, 0},
		{"negative budget", 100, -1},
		{"negative budget huge used", max, -max},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPressure(tc.used, tc.budget); got != levelLow {
				t.Fatalf("ClassifyPressure(%d, %d) = %v, want levelLow（不设限）", tc.used, tc.budget, got)
			}
		})
	}
}

func TestClassifyPressureOverBudget(t *testing.T) {
	max := pressureMaxInt()
	cases := []struct {
		name         string
		used, budget int
	}{
		{"exactly budget", 100, 100},
		{"just over", 101, 100},
		{"twice budget", 200, 100},
		{"huge used small budget", max, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPressure(tc.used, tc.budget); got != levelCritical {
				t.Fatalf("%s: ClassifyPressure(%d, %d) = %v, want levelCritical",
					tc.name, tc.used, tc.budget, got)
			}
		})
	}
}

func TestClassifyPressureCrossMultiplyNoOverflow(t *testing.T) {
	// 以 big.Rat 精确计算为差分预言机，覆盖接近 math.MaxInt 的入参：
	// 若实现里用 int 直接做 used*20，这些用例会溢出成错误等级。
	max := pressureMaxInt()
	budgets := []int{max, max - 1, max / 3, 1 << 40, 1 << 30, 7}
	fracs := [][2]int64{{1, 2}, {7, 10}, {17, 20}, {19, 20}}
	for _, budget := range budgets {
		if budget <= 0 {
			continue
		}
		for _, f := range fracs {
			// base = floor(budget * f[0] / f[1])，用 big.Int 精确求出探测基点。
			prod := new(big.Int).Mul(big.NewInt(int64(budget)), big.NewInt(f[0]))
			base := new(big.Int).Quo(prod, big.NewInt(f[1]))
			if !base.IsInt64() {
				continue
			}
			for _, delta := range []int{-1, 0, 1} {
				used := int(base.Int64()) + delta
				if used <= 0 || used > max {
					continue
				}
				got := ClassifyPressure(used, budget)
				want := classifyPressureOracle(used, budget)
				if got != want {
					t.Fatalf("ClassifyPressure(%d, %d) = %v (%s), want %v (%s); probe budget*%d/%d",
						used, budget, got, PressureLabel(got), want, PressureLabel(want), f[0], f[1])
				}
			}
		}
	}
}

// classifyPressureOracle 以 big.Rat 精确比较比例，作为整数实现的差分预言机。
// 仅供测试使用；生产实现不依赖 math/big。
func classifyPressureOracle(used, budget int) PressureLevel {
	if budget <= 0 || used <= 0 {
		return levelLow
	}
	r := new(big.Rat).SetFrac(big.NewInt(int64(used)), big.NewInt(int64(budget)))
	switch {
	case r.Cmp(big.NewRat(1, 2)) < 0:
		return levelLow
	case r.Cmp(big.NewRat(7, 10)) < 0:
		return levelNormal
	case r.Cmp(big.NewRat(17, 20)) < 0:
		return levelElevated
	case r.Cmp(big.NewRat(19, 20)) < 0:
		return levelHigh
	default:
		return levelCritical
	}
}

func TestClassifyPressureMonotonic(t *testing.T) {
	// 固定预算、用量递增时等级只升不降，确认四个阈值顺序正确。
	const budget = 1000
	prev := levelLow
	for used := 0; used <= 1200; used++ {
		got := ClassifyPressure(used, budget)
		if got < prev {
			t.Fatalf("等级非单调：used=%d 得到 %v，低于前一次 %v", used, got, prev)
		}
		prev = got
	}
}

func TestClassifyPressureDeterministic(t *testing.T) {
	cases := [][2]int{
		{0, 100}, {49, 100}, {50, 100}, {69, 100}, {70, 100},
		{84, 100}, {85, 100}, {94, 100}, {95, 100}, {200, 100}, {5, 0},
	}
	for _, c := range cases {
		first := ClassifyPressure(c[0], c[1])
		for i := 0; i < 5; i++ {
			if got := ClassifyPressure(c[0], c[1]); got != first {
				t.Fatalf("ClassifyPressure(%d, %d) 不稳定：%v vs %v", c[0], c[1], got, first)
			}
		}
	}
}

func TestClassifyPressureDoesNotMutateInputs(t *testing.T) {
	used, budget := 73, 100
	wantUsed, wantBudget := used, budget
	_ = ClassifyPressure(used, budget)
	if used != wantUsed || budget != wantBudget {
		t.Fatalf("入参被修改：used=%d budget=%d，期望 %d/%d", used, budget, wantUsed, wantBudget)
	}
}

func TestPressureLabelChineseAndEnglish(t *testing.T) {
	cases := []struct {
		level   PressureLevel
		cn, eng string
	}{
		{levelLow, "低", "low"},
		{levelNormal, "正常", "normal"},
		{levelElevated, "升高", "elevated"},
		{levelHigh, "高", "high"},
		{levelCritical, "危急", "critical"},
	}
	for _, tc := range cases {
		t.Run(tc.eng, func(t *testing.T) {
			got := PressureLabel(tc.level)
			if !strings.Contains(got, tc.cn) {
				t.Fatalf("PressureLabel(%d) = %q，未包含中文标签 %q", tc.level, got, tc.cn)
			}
			if !strings.Contains(got, tc.eng) {
				t.Fatalf("PressureLabel(%d) = %q，未包含英文键 %q", tc.level, got, tc.eng)
			}
		})
	}
}

func TestPressureLabelUnknownStable(t *testing.T) {
	first := PressureLabel(PressureLevel(999))
	second := PressureLabel(PressureLevel(999))
	if first == "" || first != second {
		t.Fatalf("未知等级标签不稳定或为空：%q / %q", first, second)
	}
}
