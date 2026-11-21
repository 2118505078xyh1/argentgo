package server

import (
	"reflect"
	"sync"
	"testing"
)

// execSeed 描述一次 Upsert 调用，供表驱动测试按序重放。
type execSeed struct {
	status ExecStatus
}

// replayExec 把一组 Upsert 操作应用到看板上。
func replayExec(b *ExecStatusBoard, seeds []execSeed) {
	for _, s := range seeds {
		b.Upsert(s.status)
	}
}

func TestExecStatusBoardUpsertGet(t *testing.T) {
	tests := []struct {
		name   string
		seeds  []execSeed
		query  string
		want   ExecStatus
		wantOK bool
	}{
		{
			name:   "zero value lookup misses",
			query:  "anything",
			wantOK: false,
		},
		{
			name: "single upsert then get",
			seeds: []execSeed{{status: ExecStatus{
				ID: "a", Phase: ExecRunning, Command: "go test ./...",
				StartedMS: 1000, EndedMS: 0, ExitCode: 0,
			}}},
			query: "a",
			want: ExecStatus{
				ID: "a", Phase: ExecRunning, Command: "go test ./...",
				StartedMS: 1000, EndedMS: 0, ExitCode: 0,
			},
			wantOK: true,
		},
		{
			name: "CJK command roundtrips",
			seeds: []execSeed{{status: ExecStatus{
				ID: "中文", Phase: ExecCompleted, Command: "编译中文项目 —— 完成",
				StartedMS: 5, EndedMS: 9, ExitCode: 0,
			}}},
			query: "中文",
			want: ExecStatus{
				ID: "中文", Phase: ExecCompleted, Command: "编译中文项目 —— 完成",
				StartedMS: 5, EndedMS: 9, ExitCode: 0,
			},
			wantOK: true,
		},
		{
			name: "later upsert replaces earlier by id",
			seeds: []execSeed{
				{status: ExecStatus{ID: "a", Phase: ExecQueued, Command: "old", StartedMS: 1}},
				{status: ExecStatus{ID: "a", Phase: ExecFailed, Command: "new", StartedMS: 2, EndedMS: 3, ExitCode: 7}},
			},
			query:  "a",
			want:   ExecStatus{ID: "a", Phase: ExecFailed, Command: "new", StartedMS: 2, EndedMS: 3, ExitCode: 7},
			wantOK: true,
		},
		{
			name: "missing among many",
			seeds: []execSeed{
				{status: ExecStatus{ID: "a", Phase: ExecQueued}},
				{status: ExecStatus{ID: "b", Phase: ExecRunning}},
			},
			query:  "missing",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b ExecStatusBoard
			replayExec(&b, tt.seeds)
			got, ok := b.Get(tt.query)
			if ok != tt.wantOK {
				t.Fatalf("Get(%q) ok = %v, want %v", tt.query, ok, tt.wantOK)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Get(%q) = %#v, want %#v", tt.query, got, tt.want)
			}
		})
	}
}

func TestExecStatusBoardListOrdering(t *testing.T) {
	tests := []struct {
		name  string
		seeds []execSeed
		want  []ExecStatus
	}{
		{
			name: "zero value returns empty non-nil slice",
			want: []ExecStatus{},
		},
		{
			name: "sorted by StartedMS ascending regardless of insert order",
			seeds: []execSeed{
				{status: ExecStatus{ID: "c", StartedMS: 30}},
				{status: ExecStatus{ID: "a", StartedMS: 10}},
				{status: ExecStatus{ID: "b", StartedMS: 20}},
			},
			want: []ExecStatus{
				{ID: "a", StartedMS: 10},
				{ID: "b", StartedMS: 20},
				{ID: "c", StartedMS: 30},
			},
		},
		{
			name: "equal StartedMS tie-breaks by ID ascending",
			seeds: []execSeed{
				{status: ExecStatus{ID: "zeta", StartedMS: 7}},
				{status: ExecStatus{ID: "alpha", StartedMS: 7}},
				{status: ExecStatus{ID: "mid", StartedMS: 7}},
			},
			want: []ExecStatus{
				{ID: "alpha", StartedMS: 7},
				{ID: "mid", StartedMS: 7},
				{ID: "zeta", StartedMS: 7},
			},
		},
		{
			name: "mixed StartedMS and ID tie-break with CJK ids",
			seeds: []execSeed{
				{status: ExecStatus{ID: "乙", StartedMS: 1}},
				{status: ExecStatus{ID: "b", StartedMS: 1}},
				{status: ExecStatus{ID: "甲", StartedMS: 0}},
				{status: ExecStatus{ID: "a", StartedMS: 2}},
			},
			want: []ExecStatus{
				{ID: "甲", StartedMS: 0},
				{ID: "b", StartedMS: 1},
				{ID: "乙", StartedMS: 1},
				{ID: "a", StartedMS: 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b ExecStatusBoard
			replayExec(&b, tt.seeds)
			got := b.List()
			if got == nil {
				t.Fatal("List() 返回 nil，期望非 nil 切片")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("List() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestExecStatusBoardRemove(t *testing.T) {
	base := []execSeed{
		{status: ExecStatus{ID: "a", Phase: ExecRunning, StartedMS: 1}},
		{status: ExecStatus{ID: "b", Phase: ExecQueued, StartedMS: 2}},
		{status: ExecStatus{ID: "c", Phase: ExecCompleted, StartedMS: 3}},
	}

	tests := []struct {
		name   string
		remove []string
		want   []string // 期望剩下的 ID（List 已按 StartedMS 排序）
	}{
		{
			name:   "remove existing",
			remove: []string{"b"},
			want:   []string{"a", "c"},
		},
		{
			name:   "remove all",
			remove: []string{"a", "b", "c"},
			want:   []string{},
		},
		{
			name:   "remove missing is no-op",
			remove: []string{"missing"},
			want:   []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b ExecStatusBoard
			replayExec(&b, base)
			for _, id := range tt.remove {
				b.Remove(id)
			}
			got := b.List()
			ids := make([]string, 0, len(got))
			for _, s := range got {
				ids = append(ids, s.ID)
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Fatalf("剩余 ID = %#v, want %#v", ids, tt.want)
			}
		})
	}

	// 零值看板上 Remove 必须安全。
	var zero ExecStatusBoard
	zero.Remove("anything")
	if got := zero.List(); len(got) != 0 {
		t.Fatalf("零值看板 Remove 后 List() = %#v, 期望空", got)
	}
}

func TestExecStatusBoardSummary(t *testing.T) {
	tests := []struct {
		name  string
		seeds []execSeed
		want  string
	}{
		{
			name: "zero value",
			want: "执行状态：排队 0，运行中 0，已完成 0，失败 0，已取消 0；合计 0",
		},
		{
			name: "one of each known phase",
			seeds: []execSeed{
				{status: ExecStatus{ID: "1", Phase: ExecQueued}},
				{status: ExecStatus{ID: "2", Phase: ExecRunning}},
				{status: ExecStatus{ID: "3", Phase: ExecCompleted}},
				{status: ExecStatus{ID: "4", Phase: ExecFailed}},
				{status: ExecStatus{ID: "5", Phase: ExecCancelled}},
			},
			want: "执行状态：排队 1，运行中 1，已完成 1，失败 1，已取消 1；合计 5",
		},
		{
			name: "mixed counts independent of insert order",
			seeds: []execSeed{
				{status: ExecStatus{ID: "c", Phase: ExecFailed}},
				{status: ExecStatus{ID: "a", Phase: ExecRunning}},
				{status: ExecStatus{ID: "b", Phase: ExecRunning}},
				{status: ExecStatus{ID: "d", Phase: ExecQueued}},
				{status: ExecStatus{ID: "e", Phase: ExecRunning}},
			},
			want: "执行状态：排队 1，运行中 3，已完成 0，失败 1，已取消 0；合计 5",
		},
		{
			name: "unknown phases sorted and appended",
			seeds: []execSeed{
				{status: ExecStatus{ID: "u1", Phase: ExecPhase("weird")}},
				{status: ExecStatus{ID: "u2", Phase: ExecPhase("其他")}},
				{status: ExecStatus{ID: "u3", Phase: ExecPhase("weird")}},
				{status: ExecStatus{ID: "u4", Phase: ExecRunning}},
			},
			want: "执行状态：排队 0，运行中 1，已完成 0，失败 0，已取消 0，未知(weird) 2，未知(其他) 1；合计 4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b ExecStatusBoard
			replayExec(&b, tt.seeds)
			if got := b.Summary(); got != tt.want {
				t.Fatalf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecStatusBoardSummaryStable(t *testing.T) {
	seeds := []execSeed{
		{status: ExecStatus{ID: "x", Phase: ExecFailed}},
		{status: ExecStatus{ID: "y", Phase: ExecPhase("zzz")}},
		{status: ExecStatus{ID: "z", Phase: ExecRunning}},
		{status: ExecStatus{ID: "w", Phase: ExecPhase("aaa")}},
	}
	var a, b ExecStatusBoard
	replayExec(&a, seeds)
	replayExec(&b, []execSeed{seeds[3], seeds[0], seeds[2], seeds[1]})

	first := a.Summary()
	second := a.Summary()
	other := b.Summary()
	if first != second {
		t.Fatalf("Summary() 不稳定: %q vs %q", first, second)
	}
	if first != other {
		t.Fatalf("不同插入顺序产生不同汇总: %q vs %q", first, other)
	}
}

func TestExecStatusBoardDoesNotMutateInput(t *testing.T) {
	var b ExecStatusBoard
	s := ExecStatus{ID: "id", Phase: ExecRunning, Command: "原始命令", StartedMS: 1}
	b.Upsert(s)

	// 修改入参不得影响看板。
	s.Command = "已修改"
	s.Phase = ExecCompleted
	s.StartedMS = 999
	want := ExecStatus{ID: "id", Phase: ExecRunning, Command: "原始命令", StartedMS: 1}
	if got, ok := b.Get("id"); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("修改 Upsert 入参影响了内部状态: Get() = %#v, %v", got, ok)
	}

	// 修改 Get 返回值不得影响看板。
	got, _ := b.Get("id")
	got.Command = "篡改"
	if again, _ := b.Get("id"); again.Command != "原始命令" {
		t.Fatalf("修改 Get 返回值影响了内部状态: %q", again.Command)
	}

	// 修改 List 返回值（含其中元素）不得影响看板。
	list := b.List()
	list[0].Command = "篡改"
	list[0].Phase = ExecFailed
	appended := append(list, ExecStatus{ID: "injected"})
	_ = appended
	if again, _ := b.Get("id"); !reflect.DeepEqual(again, want) {
		t.Fatalf("修改 List 返回值影响了内部状态: %#v", again)
	}
	if got := b.List(); len(got) != 1 {
		t.Fatalf("List 返回值修改泄漏到看板，长度 = %d, want 1", len(got))
	}
}

func TestExecStatusBoardConcurrent(t *testing.T) {
	var b ExecStatusBoard
	const (
		workers    = 8
		iterations = 300
	)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				key := "shared"
				if id%2 == 1 {
					key = "other"
				}
				b.Upsert(ExecStatus{ID: key, Phase: ExecRunning, StartedMS: int64(i)})
				_, _ = b.Get(key)
				_ = b.List()
				_ = b.Summary()
				b.Remove("missing")
			}
		}(w)
	}
	wg.Wait()

	// 并发结束后，每个写入的键都应存在且结构完好。
	for _, key := range []string{"shared", "other"} {
		if _, ok := b.Get(key); !ok {
			t.Errorf("并发结束后缺少键 %q", key)
		}
	}
	if got := len(b.List()); got != 2 {
		t.Fatalf("并发结束后 List() 长度 = %d, want 2", got)
	}
}
