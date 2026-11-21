package eval

import (
	"math"
	"reflect"
	"testing"
)

// TestScenarioCompletionRate 覆盖场景完成率的映射语义：只认 Result.Passed，
// 复用 TaskCompletionRate；空输入为 0（NaN 守卫）；且不修改入参。
func TestScenarioCompletionRate(t *testing.T) {
	pass := Result{Passed: true}
	fail := Result{Passed: false}
	// 其他字段不应影响完成率：只有 Passed 被映射为 Completed。
	passWithDetails := Result{
		Index: 7, Name: "with details", Passed: true, Reason: "should be ignored",
		Tools: []string{"read"}, Text: "ok", Turns: 3, Stopped: true,
	}

	for _, tc := range []struct {
		name    string
		results []Result
		want    float64
	}{
		{"empty nil", nil, 0},
		{"empty slice", []Result{}, 0},
		{"all pass", []Result{pass, pass}, 1},
		{"all fail", []Result{fail, fail}, 0},
		{"pass and fail", []Result{pass, fail}, 0.5},
		{"one of three", []Result{pass, fail, fail}, 1.0 / 3.0},
		{"pass with other fields", []Result{passWithDetails, fail}, 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 保留 nil 与空切片的区分，避免 DeepEqual 把 []Result{} 误判为被改动。
			var before []Result
			if tc.results != nil {
				before = append([]Result{}, tc.results...)
			}
			got := ScenarioCompletionRate(tc.results)
			if math.IsNaN(got) || math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("ScenarioCompletionRate() = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.results, before) {
				t.Fatalf("results mutated: %+v", tc.results)
			}
		})
	}
}

// TestScenarioCompletionRateDeterministic 证明相同入参重复调用得到完全一致的结果。
func TestScenarioCompletionRateDeterministic(t *testing.T) {
	results := []Result{{Passed: true}, {Passed: false}, {Passed: true}}
	first := ScenarioCompletionRate(results)
	second := ScenarioCompletionRate(results)
	if math.IsNaN(first) || first != second {
		t.Fatalf("not deterministic: first = %v, second = %v", first, second)
	}
}
