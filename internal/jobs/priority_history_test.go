package jobs

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// histItems 按 ID 列表构造条目切片，保留 nil 语义。
func histItems(ids []string) []PriorityItem {
	if ids == nil {
		return nil
	}
	out := make([]PriorityItem, len(ids))
	for i, id := range ids {
		out[i] = PriorityItem{ID: id}
	}
	return out
}

// histSnap 用给定 ID 列表构造一份快照：ID 放在 Head 中，Total 为条目数。
// 无参调用时返回零值快照，便于表示空队列视图。
func histSnap(ids ...string) PrioritySnapshot {
	head := histItems(ids)
	snap := PrioritySnapshot{Total: len(ids), Head: head}
	if len(ids) > 0 {
		snap.ByPriority = map[int]int{0: len(ids)}
	}
	return snap
}

// histIDs 提取快照条目的 ID 序列，保留 nil 与非 nil 空切片的区分。
func histIDs(items []PriorityItem) []string {
	if items == nil {
		return nil
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}

// assertTimeline 按 want 中每份快照的 Head ID 序列断言 Timeline 的内容。
func assertTimeline(t *testing.T, got []PrioritySnapshot, want [][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Timeline 长度 = %d, want %d（got=%v）", len(got), len(want), got)
	}
	for i := range want {
		if ids := histIDs(got[i].Head); !reflect.DeepEqual(ids, want[i]) {
			t.Fatalf("Timeline[%d] Head ID = %v, want %v", i, ids, want[i])
		}
	}
}

// TestSnapshotHistoryPushTimelineRing 表驱动验证 Push/Timeline 的入队顺序、
// 环形回绕、容量上限、零值与负数容量钳制、CJK ID 与空历史。
func TestSnapshotHistoryPushTimelineRing(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		pushIDs  [][]string
		wantIDs  [][]string
	}{
		{
			name:     "空历史Timeline为nil",
			capacity: 3,
			pushIDs:  nil,
			wantIDs:  nil,
		},
		{
			name:     "未写满时按入队顺序返回",
			capacity: 3,
			pushIDs:  [][]string{{"a"}, {"b"}},
			wantIDs:  [][]string{{"a"}, {"b"}},
		},
		{
			name:     "恰好写满不丢弃",
			capacity: 3,
			pushIDs:  [][]string{{"a"}, {"b"}, {"c"}},
			wantIDs:  [][]string{{"a"}, {"b"}, {"c"}},
		},
		{
			name:     "回绕一项丢弃最旧",
			capacity: 3,
			pushIDs:  [][]string{{"a"}, {"b"}, {"c"}, {"d"}},
			wantIDs:  [][]string{{"b"}, {"c"}, {"d"}},
		},
		{
			name:     "多次回绕只保留最近capacity份",
			capacity: 3,
			pushIDs:  [][]string{{"a"}, {"b"}, {"c"}, {"d"}, {"e"}, {"f"}, {"g"}},
			wantIDs:  [][]string{{"e"}, {"f"}, {"g"}},
		},
		{
			name:     "容量1只留最新",
			capacity: 1,
			pushIDs:  [][]string{{"a"}, {"b"}, {"c"}},
			wantIDs:  [][]string{{"c"}},
		},
		{
			name:     "容量0钳制为1",
			capacity: 0,
			pushIDs:  [][]string{{"a"}, {"b"}},
			wantIDs:  [][]string{{"b"}},
		},
		{
			name:     "负容量钳制为1",
			capacity: -5,
			pushIDs:  [][]string{{"a"}, {"b"}},
			wantIDs:  [][]string{{"b"}},
		},
		{
			name:     "CJK ID回绕",
			capacity: 2,
			pushIDs:  [][]string{{"作业-北京"}, {"作业-上海"}, {"作业-广州"}},
			wantIDs:  [][]string{{"作业-上海"}, {"作业-广州"}},
		},
		{
			name:     "空快照也计入历史",
			capacity: 2,
			pushIDs:  [][]string{{"a"}, nil, {"b"}},
			wantIDs:  [][]string{nil, {"b"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewSnapshotHistory(tt.capacity)
			for _, ids := range tt.pushIDs {
				h.Push(histSnap(ids...))
			}
			assertTimeline(t, h.Timeline(), tt.wantIDs)
		})
	}
}

// TestSnapshotHistoryChangesTable 表驱动验证相邻快照的中文变化摘要：新增/移除、
// 稳定顺序、多对相邻、QueueDelta 语义（同 ID 改属性不算变化）、CJK ID、
// Head/Tail 重叠去重以及不足两份时返回 nil。
func TestSnapshotHistoryChangesTable(t *testing.T) {
	tests := []struct {
		name  string
		snaps []PrioritySnapshot
		want  []string
	}{
		{
			name:  "无快照返回nil",
			snaps: nil,
			want:  nil,
		},
		{
			name:  "单快照返回nil",
			snaps: []PrioritySnapshot{histSnap("a")},
			want:  nil,
		},
		{
			name:  "两快照相同无变化",
			snaps: []PrioritySnapshot{histSnap("a", "b"), histSnap("a", "b")},
			want:  []string{"第1→2份快照：新增[无]；移除[无]"},
		},
		{
			name:  "仅新增",
			snaps: []PrioritySnapshot{histSnap("a", "b"), histSnap("a", "b", "c")},
			want:  []string{"第1→2份快照：新增[c]；移除[无]"},
		},
		{
			name:  "仅移除",
			snaps: []PrioritySnapshot{histSnap("a", "b", "c"), histSnap("a", "c")},
			want:  []string{"第1→2份快照：新增[无]；移除[b]"},
		},
		{
			name:  "新增与移除多项且顺序稳定",
			snaps: []PrioritySnapshot{histSnap("a", "b", "c"), histSnap("c", "d", "e")},
			want:  []string{"第1→2份快照：新增[d、e]；移除[a、b]"},
		},
		{
			name: "同ID改优先级与Seq不算变化",
			snaps: []PrioritySnapshot{
				{Total: 1, Head: []PriorityItem{{ID: "x", Priority: 1, Seq: 1}}},
				{Total: 1, Head: []PriorityItem{{ID: "x", Priority: 9, Seq: 99}}},
			},
			want: []string{"第1→2份快照：新增[无]；移除[无]"},
		},
		{
			name:  "CJK ID变化",
			snaps: []PrioritySnapshot{histSnap("作业-北京", "作业-上海"), histSnap("作业-上海", "作业-深圳")},
			want:  []string{"第1→2份快照：新增[作业-深圳]；移除[作业-北京]"},
		},
		{
			name: "三份快照产生两条按序摘要",
			snaps: []PrioritySnapshot{
				histSnap("a"),
				histSnap("b"),
				histSnap("b", "c"),
			},
			want: []string{
				"第1→2份快照：新增[b]；移除[a]",
				"第2→3份快照：新增[c]；移除[无]",
			},
		},
		{
			name: "Head与Tail重叠按ID去重",
			snaps: []PrioritySnapshot{
				{
					Total: 3,
					Head:  []PriorityItem{{ID: "a"}, {ID: "b"}},
					Tail:  []PriorityItem{{ID: "b"}, {ID: "c"}},
				},
				{Total: 1, Head: []PriorityItem{{ID: "c"}}},
			},
			want: []string{"第1→2份快照：新增[无]；移除[a、b]"},
		},
		{
			name: "新增顺序为Head在前Tail在后",
			snaps: []PrioritySnapshot{
				histSnap("a"),
				{Total: 3, Head: []PriorityItem{{ID: "b"}}, Tail: []PriorityItem{{ID: "c"}, {ID: "d"}}},
			},
			want: []string{"第1→2份快照：新增[b、c、d]；移除[a]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewSnapshotHistory(len(tt.snaps) + 1)
			for _, s := range tt.snaps {
				h.Push(s)
			}
			if got := h.Changes(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Changes = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSnapshotHistoryPushCopiesInput 验证 Push 不修改入参，且对入参快照的后续
// 修改（映射与切片）不会污染已保存的历史。
func TestSnapshotHistoryPushCopiesInput(t *testing.T) {
	s := PrioritySnapshot{
		Total:      2,
		ByPriority: map[int]int{1: 1, 2: 1},
		Head:       []PriorityItem{{ID: "a", Priority: 1}, {ID: "b", Priority: 2}},
		Tail:       []PriorityItem{{ID: "b", Priority: 2}},
	}
	want := PrioritySnapshot{
		Total:      2,
		ByPriority: map[int]int{1: 1, 2: 1},
		Head:       []PriorityItem{{ID: "a", Priority: 1}, {ID: "b", Priority: 2}},
		Tail:       []PriorityItem{{ID: "b", Priority: 2}},
	}

	h := NewSnapshotHistory(2)
	h.Push(s)
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("Push 修改了入参: got %v, want %v", s, want)
	}

	// 改动入参的各层底层数据，历史仍应保持 Push 时的内容。
	s.ByPriority[1] = 99
	s.Head[0].ID = "x"
	s.Tail[0].ID = "y"
	got := h.Timeline()
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("入参修改污染了历史: got %v, want %v", got, []PrioritySnapshot{want})
	}
}

// TestSnapshotHistoryTimelineReturnsCopies 验证 Timeline 返回的是独立副本：
// 修改返回切片的字段、元素或内部数据都不会写回历史。
func TestSnapshotHistoryTimelineReturnsCopies(t *testing.T) {
	h := NewSnapshotHistory(2)
	h.Push(histSnap("a", "b"))

	first := h.Timeline()
	first[0].Total = 999
	first[0].ByPriority[0] = 999
	first[0].Head[0].ID = "X"
	first[0] = PrioritySnapshot{}

	second := h.Timeline()
	if len(second) != 1 {
		t.Fatalf("Timeline 长度 = %d, want 1", len(second))
	}
	if second[0].Total != 2 {
		t.Fatalf("Total = %d, want 2", second[0].Total)
	}
	if second[0].ByPriority[0] != 2 {
		t.Fatalf("ByPriority[0] = %d, want 2", second[0].ByPriority[0])
	}
	if ids := histIDs(second[0].Head); !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("Head ID = %v, want [a b]", ids)
	}
}

// TestSnapshotHistoryChangesIsPure 验证 Changes 为纯计算：重复调用不会改变历史。
func TestSnapshotHistoryChangesIsPure(t *testing.T) {
	h := NewSnapshotHistory(4)
	h.Push(histSnap("a"))
	h.Push(histSnap("b"))
	before := h.Timeline()

	first := h.Changes()
	second := h.Changes()

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Changes 结果不稳定: first=%v second=%v", first, second)
	}
	if after := h.Timeline(); !reflect.DeepEqual(after, before) {
		t.Fatalf("Changes 修改了历史: got %v, want %v", after, before)
	}
}

// TestSnapshotHistoryZeroValue 验证零值结构体可直接 Push：容量按 1 处理，
// 查询方法与构造函数创建的历史行为一致。
func TestSnapshotHistoryZeroValue(t *testing.T) {
	var h SnapshotHistory
	if got := h.Timeline(); got != nil {
		t.Fatalf("零值 Timeline = %v, want nil", got)
	}
	if got := h.Changes(); got != nil {
		t.Fatalf("零值 Changes = %v, want nil", got)
	}

	h.Push(histSnap("a"))
	h.Push(histSnap("b"))
	h.Push(histSnap("c"))

	got := h.Timeline()
	if len(got) != 1 {
		t.Fatalf("零值历史 Timeline 长度 = %d, want 1", len(got))
	}
	if ids := histIDs(got[0].Head); !reflect.DeepEqual(ids, []string{"c"}) {
		t.Fatalf("零值历史只应保留最新快照, got %v", ids)
	}
}

// TestSnapshotHistoryConcurrent 验证多 goroutine 并发 Push/Timeline/Changes
// 时无数据竞争或内容损坏；容量足够大时应完整保留全部写入的快照。
func TestSnapshotHistoryConcurrent(t *testing.T) {
	const (
		writers   = 8
		perWriter = 50
		capacity  = writers * perWriter
	)

	h := NewSnapshotHistory(capacity)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				h.Push(histSnap(fmt.Sprintf("w%d-i%d", w, i)))
				_ = h.Timeline()
				_ = h.Changes()
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = h.Timeline()
				_ = h.Changes()
			}
		}()
	}
	wg.Wait()

	got := h.Timeline()
	if len(got) != capacity {
		t.Fatalf("并发写入后 Timeline 长度 = %d, want %d", len(got), capacity)
	}
	seen := make(map[string]bool, capacity)
	for _, snap := range got {
		id := histIDs(snap.Head)[0]
		if seen[id] {
			t.Fatalf("快照重复: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != capacity {
		t.Fatalf("唯一快照数 = %d, want %d", len(seen), capacity)
	}
}

// TestSnapshotHistoryConcurrentZeroValue 验证零值结构体在并发写入下同样安全，
// 且容量钳制为 1 后只保留最后一份快照。
func TestSnapshotHistoryConcurrentZeroValue(t *testing.T) {
	var h SnapshotHistory
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h.Push(histSnap(fmt.Sprintf("s%d", i)))
			_ = h.Changes()
		}(i)
	}
	wg.Wait()

	got := h.Timeline()
	if len(got) != 1 {
		t.Fatalf("并发零值历史 Timeline 长度 = %d, want 1", len(got))
	}
}
