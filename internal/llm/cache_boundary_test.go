package llm

import (
	"reflect"
	"testing"
)

// TestPlanCacheBoundariesBreakpointBeforeVolatile 验证 supported 且 volatileFrom
// 有效时，断点恰好落在首个易变块之前：断点索引等于 volatileFrom，代表边界位于
// 该块之前，前面的稳定块构成可复用的缓存前缀。
func TestPlanCacheBoundariesBreakpointBeforeVolatile(t *testing.T) {
	blocks := []string{"system", "history", "user-input", "tool-output"}
	got := PlanCacheBoundaries(blocks, 2, true)

	if want := []int{2}; !reflect.DeepEqual(got.Breakpoints, want) {
		t.Fatalf("Breakpoints = %v, want %v", got.Breakpoints, want)
	}
	if want := []string{"system", "history", "user-input", "tool-output"}; !reflect.DeepEqual(got.Blocks, want) {
		t.Fatalf("Blocks = %v, want %v", got.Blocks, want)
	}
}

// TestPlanCacheBoundariesUnsupported 验证 provider 不支持显式断点时不设任何断点，
// 即使 volatileFrom 有效；稳定块仍原样保留。
func TestPlanCacheBoundariesUnsupported(t *testing.T) {
	blocks := []string{"system", "history", "user-input"}
	got := PlanCacheBoundaries(blocks, 2, false)

	if len(got.Breakpoints) != 0 {
		t.Fatalf("Breakpoints = %v, want empty", got.Breakpoints)
	}
	if want := []string{"system", "history", "user-input"}; !reflect.DeepEqual(got.Blocks, want) {
		t.Fatalf("Blocks = %v, want %v", got.Blocks, want)
	}
}

// TestPlanCacheBoundariesVolatileFromEdges 覆盖 volatileFrom 的边界取值：
// 负数与越界（含等于 len）视为无效、不设断点；0 与 len-1 为有效索引，
// 分别得到位于首块之前与末块之前的断点。
func TestPlanCacheBoundariesVolatileFromEdges(t *testing.T) {
	blocks := []string{"a", "b", "c"}
	cases := []struct {
		name         string
		volatileFrom int
		want         []int
	}{
		{name: "负数无效", volatileFrom: -1},
		{name: "首块易变断点在零", volatileFrom: 0, want: []int{0}},
		{name: "中间块易变", volatileFrom: 1, want: []int{1}},
		{name: "末块易变", volatileFrom: 2, want: []int{2}},
		{name: "等于长度无效", volatileFrom: 3},
		{name: "超过长度无效", volatileFrom: 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanCacheBoundaries(blocks, tc.volatileFrom, true)
			if len(tc.want) == 0 {
				if len(got.Breakpoints) != 0 {
					t.Fatalf("Breakpoints = %v, want empty", got.Breakpoints)
				}
				return
			}
			if !reflect.DeepEqual(got.Breakpoints, tc.want) {
				t.Fatalf("Breakpoints = %v, want %v", got.Breakpoints, tc.want)
			}
		})
	}
}

// TestPlanCacheBoundariesEmptyBlocks 验证空块序列：无论 supported 与
// volatileFrom 如何取值，都不产生断点，Blocks 也保持为空。
func TestPlanCacheBoundariesEmptyBlocks(t *testing.T) {
	cases := []struct {
		name         string
		volatileFrom int
		supported    bool
	}{
		{name: "空块支持且零索引", volatileFrom: 0, supported: true},
		{name: "空块支持且正数索引", volatileFrom: 3, supported: true},
		{name: "空块不支持", volatileFrom: 0, supported: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PlanCacheBoundaries(nil, tc.volatileFrom, tc.supported)
			if len(got.Breakpoints) != 0 {
				t.Fatalf("Breakpoints = %v, want empty", got.Breakpoints)
			}
			if len(got.Blocks) != 0 {
				t.Fatalf("Blocks = %v, want empty", got.Blocks)
			}
		})
	}
}

// TestPlanCacheBoundariesDoesNotMutateInput 验证纯函数性质：返回的 Blocks 是
// 独立副本，改动它不会影响入参；反复调用只读取入参也从不改写入参。
func TestPlanCacheBoundariesDoesNotMutateInput(t *testing.T) {
	blocks := []string{"system", "history", "user-input"}
	original := append([]string(nil), blocks...)

	got := PlanCacheBoundaries(blocks, 1, true)

	// 改动返回值不应污染入参。
	got.Blocks[0] = "mutated"
	got.Breakpoints[0] = 99
	if !reflect.DeepEqual(blocks, original) {
		t.Fatalf("入参被返回值改动: blocks = %v, want %v", blocks, original)
	}

	// 对同一入参重复调用应得到一致结果（确定性）。
	again := PlanCacheBoundaries(blocks, 1, true)
	want := CacheBoundary{
		Blocks:      []string{"system", "history", "user-input"},
		Breakpoints: []int{1},
	}
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("重复调用结果 = %#v, want %#v", again, want)
	}
	if !reflect.DeepEqual(blocks, original) {
		t.Fatalf("重复调用改写了入参: blocks = %v, want %v", blocks, original)
	}
}
