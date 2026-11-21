package jobs

import (
	"reflect"
	"testing"
)

// cloneItems 复制条目切片，保留 nil 与非 nil 空切片的区分。
func cloneItems(items []PriorityItem) []PriorityItem {
	if items == nil {
		return nil
	}
	out := make([]PriorityItem, len(items))
	copy(out, items)
	return out
}

// cloneQueues 深拷贝若干队列，保留 nil 与非 nil 空切片的区分，用于验证入参
// 未被修改。
func cloneQueues(queues [][]PriorityItem) [][]PriorityItem {
	if queues == nil {
		return nil
	}
	out := make([][]PriorityItem, len(queues))
	for i, q := range queues {
		out[i] = cloneItems(q)
	}
	return out
}

// TestMergeQueuesTable 表驱动验证多队列归并顺序、去重（最高优先级/最新 Seq）、
// 空输入与 CJK ID，同时确认全量条目值（含 Priority/Seq）正确。
func TestMergeQueuesTable(t *testing.T) {
	tests := []struct {
		name   string
		queues [][]PriorityItem
		want   []PriorityItem
	}{
		{
			name: "多队列按优先级降序Seq升序归并",
			queues: [][]PriorityItem{
				{{ID: "a", Priority: 5, Seq: 1}, {ID: "b", Priority: 2, Seq: 3}},
				{{ID: "c", Priority: 9, Seq: 2}, {ID: "d", Priority: 5, Seq: 0}},
				{{ID: "e", Priority: 2, Seq: 1}},
			},
			want: []PriorityItem{
				{ID: "c", Priority: 9, Seq: 2},
				{ID: "d", Priority: 5, Seq: 0},
				{ID: "a", Priority: 5, Seq: 1},
				{ID: "e", Priority: 2, Seq: 1},
				{ID: "b", Priority: 2, Seq: 3},
			},
		},
		{
			name: "重复ID保留最高优先级",
			queues: [][]PriorityItem{
				{{ID: "x", Priority: 3, Seq: 1}},
				{{ID: "x", Priority: 8, Seq: 2}},
			},
			want: []PriorityItem{{ID: "x", Priority: 8, Seq: 2}},
		},
		{
			name: "重复ID同优先级保留最新Seq",
			queues: [][]PriorityItem{
				{{ID: "x", Priority: 5, Seq: 1}, {ID: "y", Priority: 5, Seq: 2}},
				{{ID: "x", Priority: 5, Seq: 9}},
			},
			want: []PriorityItem{
				{ID: "y", Priority: 5, Seq: 2},
				{ID: "x", Priority: 5, Seq: 9},
			},
		},
		{
			name: "等键条目稳定保持平铺顺序",
			queues: [][]PriorityItem{
				{{ID: "a", Priority: 7, Seq: 1}, {ID: "b", Priority: 7, Seq: 1}},
				{{ID: "c", Priority: 7, Seq: 1}, {ID: "d", Priority: 7, Seq: 1}},
			},
			want: []PriorityItem{
				{ID: "a", Priority: 7, Seq: 1},
				{ID: "b", Priority: 7, Seq: 1},
				{ID: "c", Priority: 7, Seq: 1},
				{ID: "d", Priority: 7, Seq: 1},
			},
		},
		{
			name:   "nil与空队列返回nil",
			queues: [][]PriorityItem{nil, {}, nil},
			want:   nil,
		},
		{
			name:   "无队列参数返回nil",
			queues: nil,
			want:   nil,
		},
		{
			name: "单队列等于稳定排序副本",
			queues: [][]PriorityItem{
				{{ID: "b", Priority: 1, Seq: 2}, {ID: "a", Priority: 3, Seq: 1}},
			},
			want: []PriorityItem{
				{ID: "a", Priority: 3, Seq: 1},
				{ID: "b", Priority: 1, Seq: 2},
			},
		},
		{
			name: "CJK ID去重与排序",
			queues: [][]PriorityItem{
				{{ID: "作业-北京", Priority: 2, Seq: 1}, {ID: "作业-上海", Priority: 5, Seq: 3}},
				{{ID: "作业-北京", Priority: 5, Seq: 7}},
			},
			want: []PriorityItem{
				{ID: "作业-上海", Priority: 5, Seq: 3},
				{ID: "作业-北京", Priority: 5, Seq: 7},
			},
		},
		{
			name: "全部重复同一ID只留最高优先级与最新Seq",
			queues: [][]PriorityItem{
				{{ID: "x", Priority: 1, Seq: 1}},
				{{ID: "x", Priority: 9, Seq: 2}},
				{{ID: "x", Priority: 9, Seq: 8}},
			},
			want: []PriorityItem{{ID: "x", Priority: 9, Seq: 8}},
		},
		{
			name: "负优先级参与排序",
			queues: [][]PriorityItem{
				{{ID: "neg", Priority: -5, Seq: 1}},
				{{ID: "pos", Priority: 5, Seq: 1}, {ID: "zero", Priority: 0, Seq: 1}},
			},
			want: []PriorityItem{
				{ID: "pos", Priority: 5, Seq: 1},
				{ID: "zero", Priority: 0, Seq: 1},
				{ID: "neg", Priority: -5, Seq: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := cloneQueues(tt.queues)
			got := MergeQueues(tt.queues...)

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("MergeQueues = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.queues, original) {
				t.Fatalf("入参被修改: got %v, want %v", tt.queues, original)
			}
		})
	}
}

// TestMergeQueuesDoesNotShareBackingArray 验证返回值是独立副本：改动结果不会
// 写回任何入参队列。
func TestMergeQueuesDoesNotShareBackingArray(t *testing.T) {
	q1 := []PriorityItem{{ID: "b", Priority: 1, Seq: 2}}
	q2 := []PriorityItem{{ID: "a", Priority: 3, Seq: 1}}
	original := cloneQueues([][]PriorityItem{q1, q2})

	got := MergeQueues(q1, q2)
	got[0].ID = "modified"

	if !reflect.DeepEqual([][]PriorityItem{q1, q2}, original) {
		t.Fatalf("改动结果写回了入参: got %v, want %v", [][]PriorityItem{q1, q2}, original)
	}
}

// TestMergeQueuesDeterministic 验证含大量重复 ID 时结果恒定：去重逻辑使用 map
// 选胜出项，但输出顺序取自稳定排序后的切片，不受 map 遍历顺序影响。
func TestMergeQueuesDeterministic(t *testing.T) {
	queues := [][]PriorityItem{
		{{ID: "p", Priority: 4, Seq: 1}, {ID: "q", Priority: 4, Seq: 1}, {ID: "p", Priority: 4, Seq: 7}},
		{{ID: "q", Priority: 4, Seq: 9}, {ID: "r", Priority: 4, Seq: 1}},
	}
	first := MergeQueues(queues...)
	for i := 0; i < 20; i++ {
		if got := MergeQueues(queues...); !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次调用不一致: got %v, want %v", i+1, got, first)
		}
	}
}

// TestQueueDeltaTable 表驱动验证新增/移除的判定、稳定顺序、重复 ID 去重、
// 空输入与 CJK ID；相同 ID 的 Priority/Seq 变化不算变化。
func TestQueueDeltaTable(t *testing.T) {
	tests := []struct {
		name        string
		before      []PriorityItem
		after       []PriorityItem
		wantAdded   []string
		wantRemoved []string
	}{
		{
			name:        "新增与移除各一项且同ID属性变化不算变化",
			before:      []PriorityItem{{ID: "a", Priority: 3, Seq: 1}, {ID: "b", Priority: 2, Seq: 2}, {ID: "c", Priority: 1, Seq: 3}},
			after:       []PriorityItem{{ID: "b", Priority: 9, Seq: 9}, {ID: "c", Priority: 1, Seq: 3}, {ID: "d", Priority: 4, Seq: 1}},
			wantAdded:   []string{"d"},
			wantRemoved: []string{"a"},
		},
		{
			name:        "同ID不同优先级与Seq视为无变化",
			before:      []PriorityItem{{ID: "x", Priority: 1, Seq: 1}},
			after:       []PriorityItem{{ID: "x", Priority: 9, Seq: 2}},
			wantAdded:   nil,
			wantRemoved: nil,
		},
		{
			name:        "全部新增",
			before:      nil,
			after:       []PriorityItem{{ID: "a", Priority: 1, Seq: 1}, {ID: "b", Priority: 2, Seq: 2}},
			wantAdded:   []string{"a", "b"},
			wantRemoved: nil,
		},
		{
			name:        "全部移除",
			before:      []PriorityItem{{ID: "a", Priority: 1, Seq: 1}, {ID: "b", Priority: 2, Seq: 2}},
			after:       nil,
			wantAdded:   nil,
			wantRemoved: []string{"a", "b"},
		},
		{
			name:        "两批都空返回两个nil",
			before:      []PriorityItem{},
			after:       []PriorityItem{},
			wantAdded:   nil,
			wantRemoved: nil,
		},
		{
			name:        "稳定顺序按各自入参出现顺序",
			before:      []PriorityItem{{ID: "c", Priority: 1, Seq: 1}, {ID: "a", Priority: 1, Seq: 2}, {ID: "b", Priority: 1, Seq: 3}},
			after:       []PriorityItem{{ID: "z", Priority: 1, Seq: 1}, {ID: "y", Priority: 1, Seq: 2}, {ID: "x", Priority: 1, Seq: 3}},
			wantAdded:   []string{"z", "y", "x"},
			wantRemoved: []string{"c", "a", "b"},
		},
		{
			name:        "CJK ID",
			before:      []PriorityItem{{ID: "作业-北京", Priority: 1, Seq: 1}, {ID: "作业-上海", Priority: 2, Seq: 2}},
			after:       []PriorityItem{{ID: "作业-上海", Priority: 3, Seq: 3}, {ID: "作业-深圳", Priority: 4, Seq: 4}},
			wantAdded:   []string{"作业-深圳"},
			wantRemoved: []string{"作业-北京"},
		},
		{
			name:        "重复ID在结果中去重",
			before:      []PriorityItem{{ID: "a", Priority: 1, Seq: 1}, {ID: "a", Priority: 1, Seq: 2}, {ID: "b", Priority: 1, Seq: 3}},
			after:       []PriorityItem{{ID: "b", Priority: 1, Seq: 1}, {ID: "b", Priority: 1, Seq: 2}, {ID: "c", Priority: 1, Seq: 3}, {ID: "c", Priority: 1, Seq: 4}},
			wantAdded:   []string{"c"},
			wantRemoved: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			beforeOriginal := cloneItems(tt.before)
			afterOriginal := cloneItems(tt.after)

			added, removed := QueueDelta(tt.before, tt.after)

			if !reflect.DeepEqual(added, tt.wantAdded) {
				t.Fatalf("added = %v, want %v", added, tt.wantAdded)
			}
			if !reflect.DeepEqual(removed, tt.wantRemoved) {
				t.Fatalf("removed = %v, want %v", removed, tt.wantRemoved)
			}
			if !reflect.DeepEqual(tt.before, beforeOriginal) {
				t.Fatalf("before 入参被修改: got %v, want %v", tt.before, beforeOriginal)
			}
			if !reflect.DeepEqual(tt.after, afterOriginal) {
				t.Fatalf("after 入参被修改: got %v, want %v", tt.after, afterOriginal)
			}
		})
	}
}

// TestQueueDeltaReturnsCopies 验证返回的切片是独立副本：改动 added/removed
// 不会影响入参（字符串切片与入参结构体切片不共享元素）。
func TestQueueDeltaReturnsCopies(t *testing.T) {
	before := []PriorityItem{{ID: "old", Priority: 1, Seq: 1}}
	after := []PriorityItem{{ID: "new", Priority: 2, Seq: 2}}
	originalBefore := append([]PriorityItem(nil), before...)
	originalAfter := append([]PriorityItem(nil), after...)

	added, removed := QueueDelta(before, after)
	added[0] = "tampered"
	removed[0] = "tampered"

	if !reflect.DeepEqual(before, originalBefore) || !reflect.DeepEqual(after, originalAfter) {
		t.Fatalf("改动结果影响了入参: before=%v after=%v", before, after)
	}
}
