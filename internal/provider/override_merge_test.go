package provider_test

import (
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

func TestMergeOverlays(t *testing.T) {
	tests := []struct {
		name     string
		overlays []provider.Overlay
		want     map[string]string
	}{
		{
			name:     "nil 切片返回空 map",
			overlays: nil,
			want:     map[string]string{},
		},
		{
			name:     "空切片返回空 map",
			overlays: []provider.Overlay{},
			want:     map[string]string{},
		},
		{
			name: "高优先级覆盖低优先级并保留其余 key",
			overlays: []provider.Overlay{
				{Name: "profile", Priority: 0, Values: map[string]string{"model": "base", "temperature": "0.7"}},
				{Name: "patch", Priority: 100, Values: map[string]string{"model": "patched"}},
			},
			want: map[string]string{"model": "patched", "temperature": "0.7"},
		},
		{
			name: "负优先级仍按数值升序应用",
			overlays: []provider.Overlay{
				{Name: "late", Priority: 5, Values: map[string]string{"key": "late"}},
				{Name: "early", Priority: -10, Values: map[string]string{"key": "early"}},
			},
			want: map[string]string{"key": "late"},
		},
		{
			// 升序排序后 alpha 先应用、zeta 后应用，故同名 key 取 zeta 的值。
			name: "同优先级按 name 决胜",
			overlays: []provider.Overlay{
				{Name: "zeta", Priority: 10, Values: map[string]string{"key": "from-zeta", "zeta-only": "z"}},
				{Name: "alpha", Priority: 10, Values: map[string]string{"key": "from-alpha"}},
			},
			want: map[string]string{"key": "from-zeta", "zeta-only": "z"},
		},
		{
			name: "优先级高于 name 决胜",
			overlays: []provider.Overlay{
				{Name: "aaa", Priority: 20, Values: map[string]string{"key": "high"}},
				{Name: "zzz", Priority: 10, Values: map[string]string{"key": "low"}},
			},
			want: map[string]string{"key": "high"},
		},
		{
			name: "CJK key 与 value 正常合并",
			overlays: []provider.Overlay{
				{Name: "默认", Priority: 0, Values: map[string]string{"模型": "基线", "温度": "0.7"}},
				{Name: "补丁", Priority: 1, Values: map[string]string{"模型": "精调"}},
			},
			want: map[string]string{"模型": "精调", "温度": "0.7"},
		},
		{
			name: "nil Values 层不产生影响",
			overlays: []provider.Overlay{
				{Name: "empty", Priority: 0, Values: nil},
				{Name: "filled", Priority: 1, Values: map[string]string{"key": "value"}},
			},
			want: map[string]string{"key": "value"},
		},
		{
			name: "同名 key 的空字符串也覆盖旧值",
			overlays: []provider.Overlay{
				{Name: "first", Priority: 0, Values: map[string]string{"key": "non-empty"}},
				{Name: "second", Priority: 1, Values: map[string]string{"key": ""}},
			},
			want: map[string]string{"key": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.MergeOverlays(tt.overlays)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MergeOverlays() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// permuteOverlays 枚举切片的所有排列，用于验证合并结果与输入排列无关。
func permuteOverlays(overlays []provider.Overlay) [][]provider.Overlay {
	if len(overlays) == 0 {
		return [][]provider.Overlay{{}}
	}
	var out [][]provider.Overlay
	for i, current := range overlays {
		rest := make([]provider.Overlay, 0, len(overlays)-1)
		rest = append(rest, overlays[:i]...)
		rest = append(rest, overlays[i+1:]...)
		for _, tail := range permuteOverlays(rest) {
			perm := make([]provider.Overlay, 0, len(overlays))
			perm = append(perm, current)
			perm = append(perm, tail...)
			out = append(out, perm)
		}
	}
	return out
}

func TestMergeOverlaysShuffledInputIsDeterministic(t *testing.T) {
	layers := []provider.Overlay{
		{Name: "profile", Priority: 0, Values: map[string]string{"model": "base", "temperature": "0.7", "语言": "zh"}},
		{Name: "bundle", Priority: 10, Values: map[string]string{"model": "bundle", "max_tokens": "1024"}},
		{Name: "local", Priority: 10, Values: map[string]string{"max_tokens": "2048", "语言": "en"}},
		{Name: "patch", Priority: 20, Values: map[string]string{"model": "patch", "temperature": "0.1"}},
	}
	want := provider.MergeOverlays(layers)

	permutations := permuteOverlays(layers)
	if len(permutations) != 24 {
		t.Fatalf("排列数 = %d, want 24", len(permutations))
	}
	for i, perm := range permutations {
		got := provider.MergeOverlays(perm)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("排列 %d 的结果 %#v 与基准 %#v 不一致", i, got, want)
		}
	}
}

func TestMergeOverlaysDoesNotMutateInput(t *testing.T) {
	original := []provider.Overlay{
		{Name: "b", Priority: 10, Values: map[string]string{"k": "v-b"}},
		{Name: "a", Priority: 0, Values: map[string]string{"k": "v-a", "only": "a"}},
	}
	wantOriginal := []provider.Overlay{
		{Name: "b", Priority: 10, Values: map[string]string{"k": "v-b"}},
		{Name: "a", Priority: 0, Values: map[string]string{"k": "v-a", "only": "a"}},
	}

	got := provider.MergeOverlays(original)

	if !reflect.DeepEqual(original, wantOriginal) {
		t.Fatalf("MergeOverlays 修改了入参切片或 map: %#v", original)
	}

	// 修改返回值不应影响任何入参。
	got["k"] = "mutated"
	got["new"] = "new"
	if !reflect.DeepEqual(original, wantOriginal) {
		t.Fatalf("修改返回值影响了入参: %#v", original)
	}
}

func TestDiffOverlays(t *testing.T) {
	tests := []struct {
		name string
		base provider.Overlay
		over provider.Overlay
		want []string
	}{
		{
			name: "两侧 nil Values 无变化",
			base: provider.Overlay{Name: "base"},
			over: provider.Overlay{Name: "over"},
			want: []string{},
		},
		{
			name: "完全相同无变化",
			base: provider.Overlay{Values: map[string]string{"a": "1", "b": "2"}},
			over: provider.Overlay{Values: map[string]string{"b": "2", "a": "1"}},
			want: []string{},
		},
		{
			name: "新增 key",
			base: provider.Overlay{Values: map[string]string{"a": "1"}},
			over: provider.Overlay{Values: map[string]string{"a": "1", "b": "2"}},
			want: []string{"b"},
		},
		{
			name: "删除 key",
			base: provider.Overlay{Values: map[string]string{"a": "1", "b": "2"}},
			over: provider.Overlay{Values: map[string]string{"a": "1"}},
			want: []string{"b"},
		},
		{
			name: "值变更",
			base: provider.Overlay{Values: map[string]string{"a": "1"}},
			over: provider.Overlay{Values: map[string]string{"a": "2"}},
			want: []string{"a"},
		},
		{
			name: "空字符串与缺失视为变化（base 有）",
			base: provider.Overlay{Values: map[string]string{"present-empty": ""}},
			over: provider.Overlay{Values: map[string]string{}},
			want: []string{"present-empty"},
		},
		{
			name: "空字符串与缺失视为变化（over 有）",
			base: provider.Overlay{Values: map[string]string{}},
			over: provider.Overlay{Values: map[string]string{"present-empty": ""}},
			want: []string{"present-empty"},
		},
		{
			name: "多项变化按 key 排序",
			base: provider.Overlay{Values: map[string]string{"zeta": "1", "alpha": "1", "same": "1", "gone": "1"}},
			over: provider.Overlay{Values: map[string]string{"alpha": "2", "same": "1", "new": "1"}},
			want: []string{"alpha", "gone", "new", "zeta"},
		},
		{
			name: "CJK key 按 UTF-8 字节序稳定排序",
			base: provider.Overlay{Values: map[string]string{"中文": "1", "模型": "1"}},
			over: provider.Overlay{Values: map[string]string{"中文": "2", "温度": "1", "模型": "1"}},
			// "中"(E4 B8 AD) < "温"(E6 B8 A9)，"模型" 未变化。
			want: []string{"中文", "温度"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.DiffOverlays(tt.base, tt.over)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("DiffOverlays() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDiffOverlaysStableAcrossRepeatedCalls(t *testing.T) {
	base := provider.Overlay{Values: map[string]string{"delta": "1", "alpha": "1", "中文": "1", "zeta": "1"}}
	over := provider.Overlay{Values: map[string]string{"delta": "2", "alpha": "2", "中文": "2", "new": "1"}}
	want := []string{"alpha", "delta", "new", "zeta", "中文"}
	for i := 0; i < 50; i++ {
		if got := provider.DiffOverlays(base, over); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次调用 DiffOverlays() = %#v, want %#v", i, got, want)
		}
	}
}

func TestDiffOverlaysDoesNotMutateInput(t *testing.T) {
	base := provider.Overlay{Name: "base", Priority: 0, Values: map[string]string{"a": "1", "b": "2"}}
	over := provider.Overlay{Name: "over", Priority: 1, Values: map[string]string{"a": "2"}}
	wantBase := provider.Overlay{Name: "base", Priority: 0, Values: map[string]string{"a": "1", "b": "2"}}
	wantOver := provider.Overlay{Name: "over", Priority: 1, Values: map[string]string{"a": "2"}}

	changed := provider.DiffOverlays(base, over)
	if !reflect.DeepEqual(changed, []string{"a", "b"}) {
		t.Fatalf("DiffOverlays() = %#v, want [a b]", changed)
	}
	if !reflect.DeepEqual(base, wantBase) || !reflect.DeepEqual(over, wantOver) {
		t.Fatalf("DiffOverlays 修改了入参: base=%#v over=%#v", base, over)
	}

	// 修改返回值不应影响任何入参。
	changed[0] = "mutated"
	if !reflect.DeepEqual(base, wantBase) || !reflect.DeepEqual(over, wantOver) {
		t.Fatalf("修改返回值影响了入参: base=%#v over=%#v", base, over)
	}
}
