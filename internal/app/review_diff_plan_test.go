package app

import (
	"reflect"
	"testing"
)

// TestReviewDiffPlan 覆盖有/无提交、文件参数、CJK 路径、空输入以及去重排序
// 与稳定性：有提交只产出一条 HEAD 对比，无提交产出暂存区 + 工作区两条。
func TestReviewDiffPlan(t *testing.T) {
	tests := []struct {
		name       string
		hasCommits bool
		files      []string
		want       [][]string
	}{
		{
			name:       "有提交带文件只产出一条 HEAD 对比",
			hasCommits: true,
			files:      []string{"a.txt", "b.txt"},
			want: [][]string{
				{"diff", "HEAD", "--", "a.txt", "b.txt"},
			},
		},
		{
			name:       "有提交无文件",
			hasCommits: true,
			files:      nil,
			want: [][]string{
				{"diff", "HEAD", "--"},
			},
		},
		{
			name:       "无提交产出暂存区与工作区两条",
			hasCommits: false,
			files:      []string{"a.txt", "b.txt"},
			want: [][]string{
				{"diff", "--cached", "--", "a.txt", "b.txt"},
				{"diff", "--", "a.txt", "b.txt"},
			},
		},
		{
			name:       "无提交无文件",
			hasCommits: false,
			files:      nil,
			want: [][]string{
				{"diff", "--cached", "--"},
				{"diff", "--"},
			},
		},
		{
			name:       "CJK 路径原样透传",
			hasCommits: true,
			files:      []string{"中文.txt", "日本語/文件.go"},
			want: [][]string{
				{"diff", "HEAD", "--", "中文.txt", "日本語/文件.go"},
			},
		},
		{
			name:       "无提交时 CJK 路径同时出现在两条命令",
			hasCommits: false,
			files:      []string{"中文.txt"},
			want: [][]string{
				{"diff", "--cached", "--", "中文.txt"},
				{"diff", "--", "中文.txt"},
			},
		},
		{
			name:       "去重并按字典序排序",
			hasCommits: true,
			files:      []string{"z.txt", "a.txt", "z.txt", "b.txt", "a.txt"},
			want: [][]string{
				{"diff", "HEAD", "--", "a.txt", "b.txt", "z.txt"},
			},
		},
		{
			name:       "以连字符开头的文件名位于 -- 之后",
			hasCommits: true,
			files:      []string{"--weird.txt"},
			want: [][]string{
				{"diff", "HEAD", "--", "--weird.txt"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewDiffPlan(tt.hasCommits, tt.files)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("reviewDiffPlan(%v, %#v) = %#v, want %#v", tt.hasCommits, tt.files, got, tt.want)
			}
		})
	}
}

// TestReviewDiffPlanDoesNotMutateInput 验证规划过程不会改动调用方传入的切片，
// 以便同一份脏文件清单可被重复、稳定地消费。
func TestReviewDiffPlanDoesNotMutateInput(t *testing.T) {
	files := []string{"z.txt", "a.txt", "z.txt"}
	original := append([]string(nil), files...)

	_ = reviewDiffPlan(true, files)
	_ = reviewDiffPlan(false, files)

	if !reflect.DeepEqual(files, original) {
		t.Fatalf("reviewDiffPlan mutated input: got %#v, want %#v", files, original)
	}
}

// TestReviewDiffPlanStable 验证对同一输入重复调用产出完全一致的命令序列。
func TestReviewDiffPlanStable(t *testing.T) {
	files := []string{"b.txt", "中文.txt", "a.txt", "b.txt"}
	first := reviewDiffPlan(false, files)
	for i := 0; i < 5; i++ {
		got := reviewDiffPlan(false, files)
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("unstable plan at run %d: got %#v, want %#v", i, got, first)
		}
	}
}
