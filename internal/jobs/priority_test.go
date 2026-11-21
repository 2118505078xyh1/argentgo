package jobs

import (
	"reflect"
	"testing"
)

// idsOf 提取条目的 ID 序列，便于断言排序结果。
func idsOf(items []PriorityItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

// TestSelectByPriorityTable 用表驱动覆盖各优先级排序、同优先级按 Seq、
// limit 边界、空输入与 CJK ID。
func TestSelectByPriorityTable(t *testing.T) {
	tests := []struct {
		name  string
		items []PriorityItem
		limit int
		want  []string
	}{
		{
			name: "不同优先级按降序",
			items: []PriorityItem{
				{ID: "low", Priority: 1, Seq: 1},
				{ID: "high", Priority: 9, Seq: 2},
				{ID: "mid", Priority: 5, Seq: 3},
			},
			limit: 3,
			want:  []string{"high", "mid", "low"},
		},
		{
			name: "同优先级按Seq升序",
			items: []PriorityItem{
				{ID: "third", Priority: 3, Seq: 30},
				{ID: "first", Priority: 3, Seq: 10},
				{ID: "second", Priority: 3, Seq: 20},
			},
			limit: 3,
			want:  []string{"first", "second", "third"},
		},
		{
			name: "先比优先级再比Seq",
			items: []PriorityItem{
				{ID: "a", Priority: 1, Seq: 1},
				{ID: "b", Priority: 2, Seq: 100},
				{ID: "c", Priority: 2, Seq: 2},
				{ID: "d", Priority: 3, Seq: 999},
				{ID: "e", Priority: 1, Seq: 0},
			},
			limit: 5,
			want:  []string{"d", "c", "b", "e", "a"},
		},
		{
			name: "负优先级正确排序",
			items: []PriorityItem{
				{ID: "neg", Priority: -5, Seq: 1},
				{ID: "zero", Priority: 0, Seq: 1},
				{ID: "pos", Priority: 5, Seq: 1},
			},
			limit: 3,
			want:  []string{"pos", "zero", "neg"},
		},
		{
			name: "limit为1取最高优先级",
			items: []PriorityItem{
				{ID: "low", Priority: 1, Seq: 1},
				{ID: "high", Priority: 9, Seq: 2},
			},
			limit: 1,
			want:  []string{"high"},
		},
		{
			name: "limit恰好等于条目数返回全部",
			items: []PriorityItem{
				{ID: "low", Priority: 1, Seq: 1},
				{ID: "high", Priority: 9, Seq: 2},
				{ID: "mid", Priority: 5, Seq: 3},
			},
			limit: 3,
			want:  []string{"high", "mid", "low"},
		},
		{
			name: "limit大于条目数返回全部",
			items: []PriorityItem{
				{ID: "low", Priority: 1, Seq: 1},
				{ID: "high", Priority: 9, Seq: 2},
			},
			limit: 10,
			want:  []string{"high", "low"},
		},
		{
			name: "limit为0返回空",
			items: []PriorityItem{
				{ID: "a", Priority: 1, Seq: 1},
			},
			limit: 0,
			want:  []string{},
		},
		{
			name: "limit为负返回空",
			items: []PriorityItem{
				{ID: "a", Priority: 1, Seq: 1},
			},
			limit: -3,
			want:  []string{},
		},
		{
			name:  "nil输入返回空",
			items: nil,
			limit: 3,
			want:  []string{},
		},
		{
			name:  "空切片输入返回空",
			items: []PriorityItem{},
			limit: 3,
			want:  []string{},
		},
		{
			name: "CJK ID按优先级与Seq排序",
			items: []PriorityItem{
				{ID: "作业-北京", Priority: 1, Seq: 2},
				{ID: "作业-上海", Priority: 9, Seq: 1},
				{ID: "作业-广州", Priority: 9, Seq: 0},
			},
			limit: 3,
			want:  []string{"作业-广州", "作业-上海", "作业-北京"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := idsOf(SelectByPriority(tt.items, tt.limit))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SelectByPriority: got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSelectByPriorityStableEqualKeys 验证 Priority 与 Seq 均相同时保留入参
// 相对顺序（稳定排序），即使在 limit 截断后也如此。
func TestSelectByPriorityStableEqualKeys(t *testing.T) {
	items := []PriorityItem{
		{ID: "first", Priority: 7, Seq: 1},
		{ID: "second", Priority: 7, Seq: 1},
		{ID: "third", Priority: 7, Seq: 1},
	}
	got := idsOf(SelectByPriority(items, 2))
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestSelectByPriorityDoesNotMutateInput 验证纯函数语义：不修改入参，且返回值
// 与入参不共享底层数组（改动返回值不会写回入参）。
func TestSelectByPriorityDoesNotMutateInput(t *testing.T) {
	items := []PriorityItem{
		{ID: "c", Priority: 1, Seq: 3},
		{ID: "a", Priority: 9, Seq: 1},
		{ID: "b", Priority: 5, Seq: 2},
	}
	original := append([]PriorityItem(nil), items...)

	got := SelectByPriority(items, 2)
	if !reflect.DeepEqual(items, original) {
		t.Fatalf("入参被修改: got %v, want %v", items, original)
	}
	got[0].ID = "modified"
	if !reflect.DeepEqual(items, original) {
		t.Fatalf("返回值与入参共享底层数组: got %v, want %v", items, original)
	}
}

// TestSelectByPriorityDeterministic 验证同一入参重复调用结果恒定。
func TestSelectByPriorityDeterministic(t *testing.T) {
	items := []PriorityItem{
		{ID: "a", Priority: 2, Seq: 5},
		{ID: "b", Priority: 2, Seq: 1},
		{ID: "c", Priority: 3, Seq: 9},
	}
	first := idsOf(SelectByPriority(items, 2))
	for i := 0; i < 5; i++ {
		if got := idsOf(SelectByPriority(items, 2)); !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次调用不一致: got %v, want %v", i+1, got, first)
		}
	}
}

// TestPrioritySummaryTable 表驱动验证各优先级计数，含空输入、负优先级与 CJK ID。
func TestPrioritySummaryTable(t *testing.T) {
	tests := []struct {
		name  string
		items []PriorityItem
		want  map[int]int
	}{
		{
			name:  "空输入返回空映射",
			items: nil,
			want:  map[int]int{},
		},
		{
			name: "单一优先级",
			items: []PriorityItem{
				{ID: "a", Priority: 3},
				{ID: "b", Priority: 3},
			},
			want: map[int]int{3: 2},
		},
		{
			name: "多优先级含负数",
			items: []PriorityItem{
				{ID: "a", Priority: 1},
				{ID: "b", Priority: 2},
				{ID: "c", Priority: 2},
				{ID: "d", Priority: -1},
			},
			want: map[int]int{1: 1, 2: 2, -1: 1},
		},
		{
			name: "CJK ID不影响计数",
			items: []PriorityItem{
				{ID: "作业-北京", Priority: 5},
				{ID: "作业-上海", Priority: 5},
				{ID: "作业-广州", Priority: 0},
			},
			want: map[int]int{5: 2, 0: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]PriorityItem(nil), tt.items...)
			got := PrioritySummary(tt.items)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("PrioritySummary: got %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.items, original) {
				t.Fatalf("入参被修改: got %v, want %v", tt.items, original)
			}
		})
	}
}

// TestPriorityTypeIsIntBased 验证 Priority 是基于 int 的命名类型，且可与
// PriorityItem.Priority（int）显式互操作。
func TestPriorityTypeIsIntBased(t *testing.T) {
	var p Priority = 7
	if int(p) != 7 {
		t.Fatalf("int(Priority(7)) = %d, want 7", int(p))
	}
	item := PriorityItem{ID: "x", Priority: int(p), Seq: 1}
	if got := PrioritySummary([]PriorityItem{item})[7]; got != 1 {
		t.Fatalf("优先级计数 = %d, want 1", got)
	}
}
