package jobs

import (
	"reflect"
	"testing"
)

// assertIDs 按 want 断言条目 ID 序列；want 为 nil 时要求 got 也为 nil。
func assertIDs(t *testing.T, label string, got []PriorityItem, want []string) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s: got %v, want nil", label, got)
		}
		return
	}
	if gotIDs := idsOf(got); !reflect.DeepEqual(gotIDs, want) {
		t.Fatalf("%s: got %v, want %v", label, gotIDs, want)
	}
}

// TestSnapshotItemsTable 表驱动验证计数、头尾截取、nil/空、边界与 CJK ID。
func TestSnapshotItemsTable(t *testing.T) {
	tests := []struct {
		name       string
		items      []PriorityItem
		headN      int
		tailN      int
		wantTotal  int
		wantByPrio map[int]int
		wantHead   []string
		wantTail   []string
	}{
		{
			name: "多优先级计数与头尾",
			items: []PriorityItem{
				{ID: "low", Priority: 1, Seq: 3},
				{ID: "high", Priority: 9, Seq: 2},
				{ID: "mid", Priority: 5, Seq: 1},
				{ID: "high2", Priority: 9, Seq: 1},
			},
			headN:      2,
			tailN:      2,
			wantTotal:  4,
			wantByPrio: map[int]int{9: 2, 5: 1, 1: 1},
			wantHead:   []string{"high2", "high"},
			wantTail:   []string{"mid", "low"},
		},
		{
			name: "同优先级按Seq且头尾重叠",
			items: []PriorityItem{
				{ID: "a", Priority: 3, Seq: 30},
				{ID: "b", Priority: 3, Seq: 10},
				{ID: "c", Priority: 3, Seq: 20},
				{ID: "d", Priority: 3, Seq: 5},
			},
			headN:      3,
			tailN:      3,
			wantTotal:  4,
			wantByPrio: map[int]int{3: 4},
			wantHead:   []string{"d", "b", "c"},
			wantTail:   []string{"b", "c", "a"},
		},
		{
			name: "头尾参数为0或负返回nil",
			items: []PriorityItem{
				{ID: "a", Priority: 1, Seq: 1},
				{ID: "b", Priority: 2, Seq: 2},
			},
			headN:      0,
			tailN:      -1,
			wantTotal:  2,
			wantByPrio: map[int]int{1: 1, 2: 1},
			wantHead:   nil,
			wantTail:   nil,
		},
		{
			name: "头尾参数超过长度取全部",
			items: []PriorityItem{
				{ID: "a", Priority: 2, Seq: 1},
				{ID: "b", Priority: 1, Seq: 2},
			},
			headN:      10,
			tailN:      99,
			wantTotal:  2,
			wantByPrio: map[int]int{2: 1, 1: 1},
			wantHead:   []string{"a", "b"},
			wantTail:   []string{"a", "b"},
		},
		{
			name:       "nil输入返回零值",
			items:      nil,
			headN:      2,
			tailN:      2,
			wantTotal:  0,
			wantByPrio: nil,
			wantHead:   nil,
			wantTail:   nil,
		},
		{
			name:       "空切片输入返回零值",
			items:      []PriorityItem{},
			headN:      2,
			tailN:      2,
			wantTotal:  0,
			wantByPrio: nil,
			wantHead:   nil,
			wantTail:   nil,
		},
		{
			name: "CJK ID按优先级与Seq截取",
			items: []PriorityItem{
				{ID: "作业-北京", Priority: 1, Seq: 2},
				{ID: "作业-上海", Priority: 9, Seq: 1},
				{ID: "作业-广州", Priority: 9, Seq: 0},
			},
			headN:      2,
			tailN:      1,
			wantTotal:  3,
			wantByPrio: map[int]int{9: 2, 1: 1},
			wantHead:   []string{"作业-广州", "作业-上海"},
			wantTail:   []string{"作业-北京"},
		},
		{
			name: "单条目头尾均取自身",
			items: []PriorityItem{
				{ID: "solo", Priority: 7, Seq: 0},
			},
			headN:      1,
			tailN:      1,
			wantTotal:  1,
			wantByPrio: map[int]int{7: 1},
			wantHead:   []string{"solo"},
			wantTail:   []string{"solo"},
		},
		{
			name: "负优先级计入统计",
			items: []PriorityItem{
				{ID: "neg", Priority: -5, Seq: 1},
				{ID: "zero", Priority: 0, Seq: 1},
				{ID: "pos", Priority: 5, Seq: 1},
			},
			headN:      1,
			tailN:      1,
			wantTotal:  3,
			wantByPrio: map[int]int{-5: 1, 0: 1, 5: 1},
			wantHead:   []string{"pos"},
			wantTail:   []string{"neg"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 保留入参的 nil 与非 nil 区分，避免空切片被误判为被修改。
			var original []PriorityItem
			if tt.items != nil {
				original = make([]PriorityItem, len(tt.items))
				copy(original, tt.items)
			}
			got := SnapshotItems(tt.items, tt.headN, tt.tailN)

			if got.Total != tt.wantTotal {
				t.Fatalf("Total = %d, want %d", got.Total, tt.wantTotal)
			}
			if tt.wantByPrio == nil {
				if got.ByPriority != nil {
					t.Fatalf("ByPriority = %v, want nil", got.ByPriority)
				}
			} else if !reflect.DeepEqual(got.ByPriority, tt.wantByPrio) {
				t.Fatalf("ByPriority = %v, want %v", got.ByPriority, tt.wantByPrio)
			}
			assertIDs(t, "Head", got.Head, tt.wantHead)
			assertIDs(t, "Tail", got.Tail, tt.wantTail)

			if !reflect.DeepEqual(tt.items, original) {
				t.Fatalf("入参被修改: got %v, want %v", tt.items, original)
			}
		})
	}
}

// TestSnapshotItemsDoesNotMutateInput 验证纯函数语义：入参不被修改，且改动
// Head/Tail 不会写回入参。
func TestSnapshotItemsDoesNotMutateInput(t *testing.T) {
	items := []PriorityItem{
		{ID: "c", Priority: 1, Seq: 3},
		{ID: "a", Priority: 9, Seq: 1},
		{ID: "b", Priority: 5, Seq: 2},
	}
	original := append([]PriorityItem(nil), items...)

	snap := SnapshotItems(items, 2, 2)
	snap.Head[0].ID = "head-modified"
	snap.Tail[0].ID = "tail-modified"

	if !reflect.DeepEqual(items, original) {
		t.Fatalf("入参被修改: got %v, want %v", items, original)
	}
}

// TestSnapshotItemsHeadTailIndependentWhenOverlapping 验证 headN+tailN 超过
// 条目数导致 Head/Tail 覆盖相同条目时，二者仍是独立副本，互不影响。
func TestSnapshotItemsHeadTailIndependentWhenOverlapping(t *testing.T) {
	items := []PriorityItem{
		{ID: "a", Priority: 3, Seq: 30},
		{ID: "b", Priority: 3, Seq: 10},
		{ID: "c", Priority: 3, Seq: 20},
		{ID: "d", Priority: 3, Seq: 5},
	}
	snap := SnapshotItems(items, 3, 3)

	if got := idsOf(snap.Head); !reflect.DeepEqual(got, []string{"d", "b", "c"}) {
		t.Fatalf("Head = %v, want [d b c]", got)
	}
	if got := idsOf(snap.Tail); !reflect.DeepEqual(got, []string{"b", "c", "a"}) {
		t.Fatalf("Tail = %v, want [b c a]", got)
	}

	snap.Head[1].ID = "X" // 修改 Head 中原本的 "b"
	if snap.Tail[0].ID != "b" {
		t.Fatalf("修改 Head 影响了 Tail: got %q, want b", snap.Tail[0].ID)
	}
}

// TestSnapshotItemsDeterministic 验证同一入参重复调用结果恒定。
func TestSnapshotItemsDeterministic(t *testing.T) {
	items := []PriorityItem{
		{ID: "a", Priority: 2, Seq: 5},
		{ID: "b", Priority: 2, Seq: 1},
		{ID: "c", Priority: 3, Seq: 9},
	}
	first := SnapshotItems(items, 2, 1)
	for i := 0; i < 5; i++ {
		if got := SnapshotItems(items, 2, 1); !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次调用不一致: got %v, want %v", i+1, got, first)
		}
	}
}

// TestSnapshotQueueNilAndEmpty 验证 nil 队列与空队列返回零值快照。
func TestSnapshotQueueNilAndEmpty(t *testing.T) {
	if got := SnapshotQueue(nil, 2, 2); !reflect.DeepEqual(got, PrioritySnapshot{}) {
		t.Fatalf("nil 队列: got %v, want 零值", got)
	}

	q := PriorityQueue(nil)
	if got := SnapshotQueue(q, 2, 2); !reflect.DeepEqual(got, PrioritySnapshot{}) {
		t.Fatalf("空队列: got %v, want 零值", got)
	}
	if q.Len() != 0 {
		t.Fatalf("空队列 Len = %d, want 0", q.Len())
	}
}

// TestSnapshotQueueReflectsPushOrder 验证快照反映 Push 后的排序内容，且查询
// 不修改队列、返回值与队列不共享底层数组。
func TestSnapshotQueueReflectsPushOrder(t *testing.T) {
	q := &priorityQueue{}
	q.Push(PriorityItem{ID: "low", Priority: 1, Seq: 1})
	q.Push(PriorityItem{ID: "high", Priority: 9, Seq: 2})
	q.Push(PriorityItem{ID: "mid", Priority: 5, Seq: 3})
	before := q.Snapshot()

	got := SnapshotQueue(q, 2, 1)

	if got.Total != 3 {
		t.Fatalf("Total = %d, want 3", got.Total)
	}
	if want := (map[int]int{9: 1, 5: 1, 1: 1}); !reflect.DeepEqual(got.ByPriority, want) {
		t.Fatalf("ByPriority = %v, want %v", got.ByPriority, want)
	}
	assertIDs(t, "Head", got.Head, []string{"high", "mid"})
	assertIDs(t, "Tail", got.Tail, []string{"low"})

	if !reflect.DeepEqual(q.Snapshot(), before) {
		t.Fatalf("队列被修改: got %v, want %v", q.Snapshot(), before)
	}

	got.Head[0].ID = "X"
	if head := q.Snapshot()[0].ID; head != "high" {
		t.Fatalf("改动 Head 影响了队列: got %q, want high", head)
	}
}
