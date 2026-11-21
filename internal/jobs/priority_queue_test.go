package jobs

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// TestPriorityQueuePushPopOrder 表驱动验证逐个 Push 后按优先级顺序 Pop，
// 排序规则与 SelectByPriority 一致。
func TestPriorityQueuePushPopOrder(t *testing.T) {
	tests := []struct {
		name  string
		items []PriorityItem
		want  []string
	}{
		{
			name: "混合优先级与同Seq",
			items: []PriorityItem{
				{ID: "a", Priority: 1, Seq: 5},
				{ID: "b", Priority: 3, Seq: 2},
				{ID: "c", Priority: 3, Seq: 1},
				{ID: "d", Priority: 1, Seq: 5},
				{ID: "e", Priority: 2, Seq: 0},
			},
			want: []string{"c", "b", "e", "a", "d"},
		},
		{
			name: "不同优先级降序",
			items: []PriorityItem{
				{ID: "low", Priority: 1, Seq: 1},
				{ID: "high", Priority: 9, Seq: 2},
				{ID: "mid", Priority: 5, Seq: 3},
			},
			want: []string{"high", "mid", "low"},
		},
		{
			name: "同优先级按Seq升序",
			items: []PriorityItem{
				{ID: "third", Priority: 3, Seq: 30},
				{ID: "first", Priority: 3, Seq: 10},
				{ID: "second", Priority: 3, Seq: 20},
			},
			want: []string{"first", "second", "third"},
		},
		{
			name: "负优先级正确排序",
			items: []PriorityItem{
				{ID: "neg", Priority: -5, Seq: 1},
				{ID: "zero", Priority: 0, Seq: 1},
				{ID: "pos", Priority: 5, Seq: 1},
			},
			want: []string{"pos", "zero", "neg"},
		},
		{
			name:  "空输入无输出",
			items: nil,
			want:  []string{},
		},
		{
			name: "CJK ID按优先级与Seq排序",
			items: []PriorityItem{
				{ID: "作业-北京", Priority: 1, Seq: 2},
				{ID: "作业-上海", Priority: 9, Seq: 1},
				{ID: "作业-广州", Priority: 9, Seq: 0},
			},
			want: []string{"作业-广州", "作业-上海", "作业-北京"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := &priorityQueue{}
			for _, item := range tt.items {
				q.Push(item)
			}
			got := make([]string, 0)
			for {
				item, ok := q.Pop()
				if !ok {
					break
				}
				got = append(got, item.ID)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Push/Pop 顺序: got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPriorityQueueConstructorMatchesPush 验证构造函数的初始排序结果与
// 逐个 Push 得到的内容一致，且构造函数不修改入参。
func TestPriorityQueueConstructorMatchesPush(t *testing.T) {
	items := []PriorityItem{
		{ID: "a", Priority: 1, Seq: 5},
		{ID: "b", Priority: 3, Seq: 2},
		{ID: "c", Priority: 3, Seq: 1},
		{ID: "d", Priority: 1, Seq: 5},
		{ID: "e", Priority: 2, Seq: 0},
	}
	original := append([]PriorityItem(nil), items...)

	q := PriorityQueue(items)
	if !reflect.DeepEqual(items, original) {
		t.Fatalf("构造函数修改了入参: got %v, want %v", items, original)
	}
	got := idsOf(q.Snapshot())
	want := []string{"c", "b", "e", "a", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("构造排序: got %v, want %v", got, want)
	}

	pushQ := &priorityQueue{}
	for _, item := range items {
		pushQ.Push(item)
	}
	if !reflect.DeepEqual(q.Snapshot(), pushQ.Snapshot()) {
		t.Fatalf("构造与逐条 Push 结果不一致: %v vs %v", q.Snapshot(), pushQ.Snapshot())
	}
}

// TestPriorityQueueSnapshotIsCopy 验证 Snapshot 返回有序副本，且修改副本
// 不会写回队列内部状态。
func TestPriorityQueueSnapshotIsCopy(t *testing.T) {
	q := PriorityQueue([]PriorityItem{
		{ID: "low", Priority: 1, Seq: 1},
		{ID: "high", Priority: 9, Seq: 2},
	})
	snap := q.Snapshot()
	if got := idsOf(snap); !reflect.DeepEqual(got, []string{"high", "low"}) {
		t.Fatalf("Snapshot 顺序: got %v", got)
	}
	snap[0].ID = "modified"
	snap[0].Priority = -100

	again := q.Snapshot()
	if got := idsOf(again); !reflect.DeepEqual(got, []string{"high", "low"}) {
		t.Fatalf("修改副本影响了队列: got %v", got)
	}
	if again[0].Priority != 9 {
		t.Fatalf("修改副本影响了队列优先级: got %d", again[0].Priority)
	}
}

// TestPriorityQueueLen 验证 Len 随 Push/Pop 正确变化，且空队列行为正确。
func TestPriorityQueueLen(t *testing.T) {
	q := &priorityQueue{}
	if q.Len() != 0 {
		t.Fatalf("初始 Len = %d, want 0", q.Len())
	}
	if _, ok := q.Pop(); ok {
		t.Fatal("空队列 Pop 应返回 false")
	}
	if item, ok := q.Pop(); ok || item != (PriorityItem{}) {
		t.Fatalf("空队列 Pop 应返回零值与 false, got %v, %v", item, ok)
	}
	if snap := q.Snapshot(); snap != nil {
		t.Fatalf("空队列 Snapshot 应为 nil, got %v", snap)
	}

	q.Push(PriorityItem{ID: "x", Priority: 1, Seq: 1})
	if q.Len() != 1 {
		t.Fatalf("Push 后 Len = %d, want 1", q.Len())
	}
	q.Push(PriorityItem{ID: "y", Priority: 2, Seq: 1})
	if q.Len() != 2 {
		t.Fatalf("再次 Push 后 Len = %d, want 2", q.Len())
	}
	if _, ok := q.Pop(); !ok {
		t.Fatal("非空队列 Pop 应返回 true")
	}
	if q.Len() != 1 {
		t.Fatalf("Pop 后 Len = %d, want 1", q.Len())
	}
}

// TestPriorityQueueStableEqualKeys 验证 Priority 与 Seq 均相同时，入队顺序
// 被保留（稳定语义），与 SelectByPriority 的行为一致。
func TestPriorityQueueStableEqualKeys(t *testing.T) {
	q := &priorityQueue{}
	for _, id := range []string{"first", "second", "third"} {
		q.Push(PriorityItem{ID: id, Priority: 7, Seq: 1})
	}
	want := []string{"first", "second", "third"}
	if got := idsOf(q.Snapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("稳定顺序: got %v, want %v", got, want)
	}
}

// TestPriorityQueueConcurrent 并发 Push/Pop/Snapshot/Len，验证线程安全与
// 最终顺序。每个条目的 Seq 唯一，因此最终顺序与并发插入顺序无关，可精确断言。
func TestPriorityQueueConcurrent(t *testing.T) {
	const total = 400
	items := make([]PriorityItem, total)
	for i := range items {
		items[i] = PriorityItem{
			ID:       fmt.Sprintf("item-%d", i),
			Priority: i % 5,
			Seq:      uint64(i + 1), // 唯一 Seq，保证全序确定
		}
	}
	want := idsOf(SortedByPriority(items))

	q := &priorityQueue{}
	var readerDone sync.WaitGroup
	readerDone.Add(1)
	readerStop := make(chan struct{})
	go func() {
		defer readerDone.Done()
		for {
			select {
			case <-readerStop:
				return
			default:
				_ = q.Len()
				_ = q.Snapshot()
			}
		}
	}()

	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := worker; i < total; i += 4 {
				q.Push(items[i])
			}
		}(worker)
	}
	wg.Wait()
	close(readerStop)
	readerDone.Wait()

	if q.Len() != total {
		t.Fatalf("并发 Push 后 Len = %d, want %d", q.Len(), total)
	}
	if got := idsOf(q.Snapshot()); !reflect.DeepEqual(got, want) {
		t.Fatalf("并发 Push 后顺序: got %v, want %v", got, want)
	}

	// 并发 Pop：多个 goroutine 争相取条目，收集到共享切片后校验总集与数量。
	// 并发收集的先后不确定，因此按集合比较，而非逐条顺序。
	var popped []PriorityItem
	var popMu sync.Mutex
	var popWg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		popWg.Add(1)
		go func() {
			defer popWg.Done()
			for {
				item, ok := q.Pop()
				if !ok {
					return
				}
				popMu.Lock()
				popped = append(popped, item)
				popMu.Unlock()
			}
		}()
	}
	popWg.Wait()

	if len(popped) != total {
		t.Fatalf("并发 Pop 数量 = %d, want %d", len(popped), total)
	}
	gotSet := make(map[string]PriorityItem, len(popped))
	for _, item := range popped {
		gotSet[item.ID] = item
	}
	if len(gotSet) != total {
		t.Fatalf("并发 Pop 出现重复 ID: 去重后 %d, want %d", len(gotSet), total)
	}
	for _, item := range items {
		if got, ok := gotSet[item.ID]; !ok || got != item {
			t.Fatalf("并发 Pop 丢失或篡改条目 %q: got %v, %v", item.ID, got, ok)
		}
	}
	if q.Len() != 0 {
		t.Fatalf("取完后 Len = %d, want 0", q.Len())
	}

	// 单独用新队列验证逐条 Pop 的确定性顺序（不受并发影响）。
	seqQ := PriorityQueue(items)
	var seqGot []string
	for {
		item, ok := seqQ.Pop()
		if !ok {
			break
		}
		seqGot = append(seqGot, item.ID)
	}
	if !reflect.DeepEqual(seqGot, want) {
		t.Fatalf("逐条 Pop 顺序: got %v, want %v", seqGot, want)
	}
}

// TestSortedByPriorityTable 验证 SortedByPriority 等价于 limit 全量的
// SelectByPriority，且不修改入参。
func TestSortedByPriorityTable(t *testing.T) {
	tests := []struct {
		name  string
		items []PriorityItem
		want  []string
	}{
		{
			name: "全量排序",
			items: []PriorityItem{
				{ID: "c", Priority: 1, Seq: 3},
				{ID: "a", Priority: 9, Seq: 1},
				{ID: "b", Priority: 5, Seq: 2},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name:  "nil输入",
			items: nil,
			want:  []string{},
		},
		{
			name: "CJK ID",
			items: []PriorityItem{
				{ID: "作业-北京", Priority: 1, Seq: 2},
				{ID: "作业-上海", Priority: 9, Seq: 1},
			},
			want: []string{"作业-上海", "作业-北京"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]PriorityItem(nil), tt.items...)
			got := idsOf(SortedByPriority(tt.items))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SortedByPriority: got %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(tt.items, original) {
				t.Fatalf("入参被修改: got %v, want %v", tt.items, original)
			}
			// 与 SelectByPriority 全量结果一致。
			viaSelect := idsOf(SelectByPriority(tt.items, len(tt.items)))
			if !reflect.DeepEqual(got, viaSelect) {
				t.Fatalf("与 SelectByPriority 不一致: %v vs %v", got, viaSelect)
			}
		})
	}
}

// TestPriorityOfTable 表驱动验证按 ID 查找优先级，含缺失、重复与 CJK ID。
func TestPriorityOfTable(t *testing.T) {
	items := []PriorityItem{
		{ID: "a", Priority: 1, Seq: 1},
		{ID: "作业-北京", Priority: 9, Seq: 2},
		{ID: "b", Priority: 5, Seq: 3},
		{ID: "b", Priority: 7, Seq: 4}, // 重复 ID 返回首个匹配
	}
	tests := []struct {
		name   string
		id     string
		want   int
		wantOK bool
	}{
		{name: "命中", id: "a", want: 1, wantOK: true},
		{name: "CJK ID命中", id: "作业-北京", want: 9, wantOK: true},
		{name: "重复ID返回首个", id: "b", want: 5, wantOK: true},
		{name: "缺失", id: "missing", want: 0, wantOK: false},
		{name: "空ID", id: "", want: 0, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PriorityOf(tt.id, items)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("PriorityOf(%q) = (%d,%v), want (%d,%v)", tt.id, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// ensure sort import is used: 显式确认 SortedByPriority 与 sort.SliceStable
// 的排序结果一致（防止实现漂移）。
func TestSortedByPriorityMatchesSortSliceStable(t *testing.T) {
	items := []PriorityItem{
		{ID: "a", Priority: 2, Seq: 5},
		{ID: "b", Priority: 2, Seq: 5},
		{ID: "c", Priority: 3, Seq: 1},
	}
	want := append([]PriorityItem(nil), items...)
	sort.SliceStable(want, func(i, j int) bool {
		return priorityLess(want[i], want[j])
	})
	got := SortedByPriority(items)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("与 sort.SliceStable 不一致: got %v, want %v", got, want)
	}
}
