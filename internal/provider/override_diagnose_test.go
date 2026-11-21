package provider_test

import (
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/provider"
)

func TestDiagnoseOverlays(t *testing.T) {
	tests := []struct {
		name string
		base provider.Overlay
		over provider.Overlay
		want []provider.OverlayDiff
	}{
		{
			name: "两侧 nil Values 无变化",
			base: provider.Overlay{Name: "base"},
			over: provider.Overlay{Name: "over"},
			want: []provider.OverlayDiff{},
		},
		{
			name: "完全相同无变化",
			base: provider.Overlay{Values: map[string]string{"a": "1", "b": "2"}},
			over: provider.Overlay{Values: map[string]string{"b": "2", "a": "1"}},
			want: []provider.OverlayDiff{},
		},
		{
			name: "新增 key",
			base: provider.Overlay{Values: map[string]string{"a": "1"}},
			over: provider.Overlay{Values: map[string]string{"a": "1", "b": "2"}},
			want: []provider.OverlayDiff{
				{Key: "b", Over: "2", Kind: provider.DiffKindAdded},
			},
		},
		{
			name: "删除 key",
			base: provider.Overlay{Values: map[string]string{"a": "1", "b": "2"}},
			over: provider.Overlay{Values: map[string]string{"a": "1"}},
			want: []provider.OverlayDiff{
				{Key: "b", Base: "2", Kind: provider.DiffKindRemoved},
			},
		},
		{
			name: "值变更",
			base: provider.Overlay{Values: map[string]string{"a": "1"}},
			over: provider.Overlay{Values: map[string]string{"a": "2"}},
			want: []provider.OverlayDiff{
				{Key: "a", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
			},
		},
		{
			name: "新增 key 的值为空字符串仍以 Kind 区分",
			base: provider.Overlay{Values: map[string]string{}},
			over: provider.Overlay{Values: map[string]string{"k": ""}},
			want: []provider.OverlayDiff{
				{Key: "k", Kind: provider.DiffKindAdded},
			},
		},
		{
			name: "删除 key 的旧值为空字符串仍以 Kind 区分",
			base: provider.Overlay{Values: map[string]string{"k": ""}},
			over: provider.Overlay{Values: map[string]string{}},
			want: []provider.OverlayDiff{
				{Key: "k", Kind: provider.DiffKindRemoved},
			},
		},
		{
			name: "混合多项按 key 排序并覆盖 CJK key",
			base: provider.Overlay{Values: map[string]string{"zeta": "1", "alpha": "1", "same": "1", "gone": "1", "中文": "旧"}},
			over: provider.Overlay{Values: map[string]string{"alpha": "2", "same": "1", "new": "1", "中文": "新", "温度": "x"}},
			// ASCII key 排在 CJK key 之前；"中"(E4 B8 AD) < "温"(E6 B8 A9)。
			want: []provider.OverlayDiff{
				{Key: "alpha", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
				{Key: "gone", Base: "1", Kind: provider.DiffKindRemoved},
				{Key: "new", Over: "1", Kind: provider.DiffKindAdded},
				{Key: "zeta", Base: "1", Kind: provider.DiffKindRemoved},
				{Key: "中文", Base: "旧", Over: "新", Kind: provider.DiffKindChanged},
				{Key: "温度", Over: "x", Kind: provider.DiffKindAdded},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.DiagnoseOverlays(tt.base, tt.over)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("DiagnoseOverlays() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDiagnoseOverlaysStableAcrossRepeatedCalls(t *testing.T) {
	base := provider.Overlay{Values: map[string]string{"delta": "1", "alpha": "1", "中文": "1", "zeta": "1"}}
	over := provider.Overlay{Values: map[string]string{"delta": "2", "alpha": "2", "中文": "2", "new": "1"}}
	want := []provider.OverlayDiff{
		{Key: "alpha", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
		{Key: "delta", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
		{Key: "new", Over: "1", Kind: provider.DiffKindAdded},
		{Key: "zeta", Base: "1", Kind: provider.DiffKindRemoved},
		{Key: "中文", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
	}
	for i := 0; i < 50; i++ {
		if got := provider.DiagnoseOverlays(base, over); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次调用 DiagnoseOverlays() = %#v, want %#v", i, got, want)
		}
	}
}

func TestDiagnoseOverlaysDoesNotMutateInput(t *testing.T) {
	base := provider.Overlay{Name: "base", Priority: 0, Values: map[string]string{"a": "1", "b": "2"}}
	over := provider.Overlay{Name: "over", Priority: 1, Values: map[string]string{"a": "2"}}
	wantBase := provider.Overlay{Name: "base", Priority: 0, Values: map[string]string{"a": "1", "b": "2"}}
	wantOver := provider.Overlay{Name: "over", Priority: 1, Values: map[string]string{"a": "2"}}

	got := provider.DiagnoseOverlays(base, over)
	if !reflect.DeepEqual(base, wantBase) || !reflect.DeepEqual(over, wantOver) {
		t.Fatalf("DiagnoseOverlays 修改了入参: base=%#v over=%#v", base, over)
	}

	// 修改返回值不应影响任何入参（OverlayDiff 仅含值类型字段）。
	got[0].Base = "mutated"
	got = append(got, provider.OverlayDiff{Key: "new"})
	if !reflect.DeepEqual(base, wantBase) || !reflect.DeepEqual(over, wantOver) {
		t.Fatalf("修改返回值影响了入参: base=%#v over=%#v", base, over)
	}
}

func TestSummarizeDiffs(t *testing.T) {
	tests := []struct {
		name  string
		diffs []provider.OverlayDiff
		want  string
	}{
		{
			name:  "nil 返回固定字面量",
			diffs: nil,
			want:  "无差异",
		},
		{
			name:  "空切片返回固定字面量",
			diffs: []provider.OverlayDiff{},
			want:  "无差异",
		},
		{
			name:  "仅新增",
			diffs: []provider.OverlayDiff{{Key: "k", Over: "v", Kind: provider.DiffKindAdded}},
			want:  "共 1 项变化：新增 1 项，删除 0 项，变更 0 项",
		},
		{
			name:  "仅删除",
			diffs: []provider.OverlayDiff{{Key: "k", Base: "v", Kind: provider.DiffKindRemoved}},
			want:  "共 1 项变化：新增 0 项，删除 1 项，变更 0 项",
		},
		{
			name:  "仅变更",
			diffs: []provider.OverlayDiff{{Key: "k", Base: "1", Over: "2", Kind: provider.DiffKindChanged}},
			want:  "共 1 项变化：新增 0 项，删除 0 项，变更 1 项",
		},
		{
			name: "混合统计含 CJK key（key 不影响计数）",
			diffs: []provider.OverlayDiff{
				{Key: "模型", Over: "x", Kind: provider.DiffKindAdded},
				{Key: "温度", Over: "y", Kind: provider.DiffKindAdded},
				{Key: "gone", Base: "1", Kind: provider.DiffKindRemoved},
				{Key: "a", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
				{Key: "b", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
				{Key: "中文", Base: "旧", Over: "新", Kind: provider.DiffKindChanged},
			},
			want: "共 6 项变化：新增 2 项，删除 1 项，变更 3 项",
		},
		{
			name:  "未知 Kind 计入总数但不落入三类",
			diffs: []provider.OverlayDiff{{Key: "k", Kind: "unknown"}},
			want:  "共 1 项变化：新增 0 项，删除 0 项，变更 0 项",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := provider.SummarizeDiffs(tt.diffs); got != tt.want {
				t.Fatalf("SummarizeDiffs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeDiffsStableAcrossRepeatedCalls(t *testing.T) {
	diffs := []provider.OverlayDiff{
		{Key: "b", Base: "1", Over: "2", Kind: provider.DiffKindChanged},
		{Key: "a", Over: "1", Kind: provider.DiffKindAdded},
		{Key: "c", Base: "1", Kind: provider.DiffKindRemoved},
	}
	want := "共 3 项变化：新增 1 项，删除 1 项，变更 1 项"
	for i := 0; i < 50; i++ {
		if got := provider.SummarizeDiffs(diffs); got != want {
			t.Fatalf("第 %d 次调用 SummarizeDiffs() = %q, want %q", i, got, want)
		}
	}
}
