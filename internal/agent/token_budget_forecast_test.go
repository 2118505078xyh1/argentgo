package agent

import (
	"math"
	"reflect"
	"testing"
)

func TestForecastNextTokensUpwardTrend(t *testing.T) {
	// 窗口 [100,200,300,400]：平均 250，斜率 +100 -> 350。
	if got := ForecastNextTokens([]int{100, 200, 300, 400}); got != 350 {
		t.Fatalf("upward forecast = %d, want 350", got)
	}
}

func TestForecastNextTokensDownwardTrend(t *testing.T) {
	// 窗口 [400,300,200,100]：平均 250，斜率 -100 -> 150。
	if got := ForecastNextTokens([]int{400, 300, 200, 100}); got != 150 {
		t.Fatalf("downward forecast = %d, want 150", got)
	}
	// 不足两步的下降也保留斜率（四舍五入），不会因向零截断而丢失趋势。
	if got := ForecastNextTokens([]int{10, 9}); got != 8 {
		t.Fatalf("small downward forecast = %d, want 8", got)
	}
}

func TestForecastNextTokensFlat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inputs []int
		want   int
	}{
		{"four samples", []int{50, 50, 50, 50}, 50},
		{"two samples", []int{7, 7}, 7},
		{"zeros", []int{0, 0, 0, 0}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ForecastNextTokens(tc.inputs); got != tc.want {
				t.Fatalf("flat forecast = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestForecastNextTokensTooFewSamples(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inputs []int
		want   int
	}{
		{"nil", nil, 0},
		{"empty", []int{}, 0},
		{"single", []int{42}, 42},
		{"negative single clamped", []int{-9}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ForecastNextTokens(tc.inputs); got != tc.want {
				t.Fatalf("forecast = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestForecastNextTokensClampsNegativesAndOverflow(t *testing.T) {
	if got := ForecastNextTokens([]int{-100, -50, -10, -1}); got != 0 {
		t.Fatalf("all-negative forecast = %d, want 0", got)
	}
	// 负数样本按 0 处理，其余正常外推：[0,10,20,30] -> 平均 15 + 斜率 10 = 25。
	if got := ForecastNextTokens([]int{math.MinInt, 10, 20, 30}); got != 25 {
		t.Fatalf("mixed negative forecast = %d, want 25", got)
	}
	// 极限值进入斜率计算时经饱和加法收敛到最大 int，不回绕成负数。
	if got := ForecastNextTokens([]int{math.MinInt, math.MaxInt}); got != math.MaxInt {
		t.Fatalf("saturating forecast = %d, want %d", got, math.MaxInt)
	}
	// 全部为 MaxInt：结果必须为正且不超过 int 上限，绝不溢出为负。
	if got := ForecastNextTokens([]int{math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt}); got <= 0 || got > math.MaxInt {
		t.Fatalf("max-int forecast = %d, want (0, %d]", got, math.MaxInt)
	}
}

func TestForecastNextTokensUsesRecentWindowOnly(t *testing.T) {
	// 早期的大用量应被窗口排除：只看 [10,20,30,40] -> 平均 25 + 斜率 10 = 35。
	if got := ForecastNextTokens([]int{1000, 1000, 10, 20, 30, 40}); got != 35 {
		t.Fatalf("windowed forecast = %d, want 35", got)
	}
}

func TestForecastNextTokensIsDeterministicAndDoesNotMutate(t *testing.T) {
	in := []int{-5, 10, 20, 30}
	snapshot := append([]int(nil), in...)

	first := ForecastNextTokens(in)
	second := ForecastNextTokens(in)
	if first != second {
		t.Fatalf("forecast is not deterministic: %d vs %d", first, second)
	}
	if !reflect.DeepEqual(in, snapshot) {
		t.Fatalf("input mutated: got %v, want %v", in, snapshot)
	}
}

func TestTokenTrendCarriesSamples(t *testing.T) {
	trend := TokenTrend{Samples: []int{1, 2, 3}}
	// [1,2,3]：平均 2，斜率 +1 -> 3。
	if got := ForecastNextTokens(trend.Samples); got != 3 {
		t.Fatalf("trend forecast = %d, want 3", got)
	}
	if !reflect.DeepEqual(trend.Samples, []int{1, 2, 3}) {
		t.Fatalf("trend samples mutated: %v", trend.Samples)
	}
}

func TestWillExceedBudget(t *testing.T) {
	for _, tc := range []struct {
		name           string
		forecast, used int
		budget         int
		want           bool
	}{
		{"below", 39, 60, 100, false},
		{"at boundary", 40, 60, 100, true},
		{"above", 41, 60, 100, true},
		{"unlimited zero", 1000, 1000, 0, false},
		{"unlimited negative", 1000, 1000, -1, false},
		{"negatives clamped", 200, -50, 100, true},
		{"overflow saturates", math.MaxInt, math.MaxInt, math.MaxInt, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WillExceedBudget(tc.forecast, tc.used, tc.budget); got != tc.want {
				t.Fatalf("WillExceedBudget(%d, %d, %d) = %v, want %v",
					tc.forecast, tc.used, tc.budget, got, tc.want)
			}
		})
	}
}

func TestSuggestTrimTokens(t *testing.T) {
	for _, tc := range []struct {
		name           string
		used, forecast int
		budget         int
		want           int
	}{
		{"exact", 60, 40, 100, 0},
		{"under", 60, 30, 100, 0},
		{"over", 60, 50, 100, 10},
		{"used alone over", 120, 0, 100, 20},
		{"unlimited", 1000, 1000, 0, 0},
		{"negative budget", 1000, 1000, -5, 0},
		{"negatives clamped", -50, 200, 100, 100},
		{"overflow no wrap", math.MaxInt, 10, math.MaxInt, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SuggestTrimTokens(tc.used, tc.forecast, tc.budget); got != tc.want {
				t.Fatalf("SuggestTrimTokens(%d, %d, %d) = %d, want %d",
					tc.used, tc.forecast, tc.budget, got, tc.want)
			}
		})
	}
}
