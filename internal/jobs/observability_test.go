package jobs

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// snap 用给定字段构造一条快照，便于表驱动用例简洁声明。
func snap(id, state string, startedMS int64) JobSnapshot {
	return JobSnapshot{
		ID:          id,
		Command:     "cmd-" + id,
		State:       state,
		StartedMS:   startedMS,
		DurationMS:  0,
		OutputBytes: 0,
	}
}

// ids 提取快照 ID 序列，保留 nil 语义。
func ids(snapshots []JobSnapshot) []string {
	if snapshots == nil {
		return nil
	}
	out := make([]string, len(snapshots))
	for i, s := range snapshots {
		out[i] = s.ID
	}
	return out
}

// TestJobBoardUpsert 验证写入与按 ID 覆盖：同 ID 后写覆盖先写，作业数不变。
func TestJobBoardUpsert(t *testing.T) {
	var b JobBoard

	b.Upsert(snap("a", JobStateRunning, 10))
	b.Upsert(snap("b", JobStateDone, 20))
	if got := ids(b.List()); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("Upsert 两条后 List ID = %v, want [a b]", got)
	}

	// 覆盖 ID=a：命令/状态/耗时/输出都应更新，StartedMS 也取新值。
	b.Upsert(JobSnapshot{
		ID:          "a",
		Command:     "新命令",
		State:       JobStateFailed,
		StartedMS:   5,
		DurationMS:  99,
		OutputBytes: 128,
	})
	list := b.List()
	if len(list) != 2 {
		t.Fatalf("覆盖同 ID 后作业数 = %d, want 2", len(list))
	}
	if list[0].ID != "a" || list[0].Command != "新命令" || list[0].State != JobStateFailed ||
		list[0].DurationMS != 99 || list[0].OutputBytes != 128 {
		t.Fatalf("覆盖后的快照 = %+v, want 新命令/failed/99/128", list[0])
	}
	run, done, failed := b.Stats()
	if run != 0 || done != 1 || failed != 1 {
		t.Fatalf("覆盖后 Stats = (%d,%d,%d), want (0,1,1)", run, done, failed)
	}
}

// TestJobBoardRemove 验证删除存在/不存在的 ID，以及空看板删除不 panic。
func TestJobBoardRemove(t *testing.T) {
	b := &JobBoard{}
	b.Remove("missing") // 空看板删除应安全

	b.Upsert(snap("a", JobStateRunning, 1))
	b.Upsert(snap("b", JobStateDone, 2))
	b.Upsert(snap("c", JobStateFailed, 3))

	b.Remove("b")
	b.Remove("not-exist")

	if got := ids(b.List()); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("Remove 后 List ID = %v, want [a c]", got)
	}
	if run, done, failed := b.Stats(); run != 1 || done != 0 || failed != 1 {
		t.Fatalf("Remove 后 Stats = (%d,%d,%d), want (1,0,1)", run, done, failed)
	}

	b.Remove("a")
	b.Remove("c")
	if got := b.List(); got != nil {
		t.Fatalf("删空后 List = %v, want nil", got)
	}
	if run, done, failed := b.Stats(); run != 0 || done != 0 || failed != 0 {
		t.Fatalf("删空后 Stats = (%d,%d,%d), want (0,0,0)", run, done, failed)
	}
}

// TestJobBoardListSort 表驱动验证 List 按 StartedMS 升序、相同 StartedMS 按 ID
// 字节序稳定排列，且与 Upsert 顺序无关。
func TestJobBoardListSort(t *testing.T) {
	tests := []struct {
		name string
		adds []JobSnapshot
		want []string
	}{
		{
			name: "空看板返回nil",
			adds: nil,
			want: nil,
		},
		{
			name: "按StartedMS升序而非插入顺序",
			adds: []JobSnapshot{
				snap("late", JobStateRunning, 30),
				snap("early", JobStateDone, 10),
				snap("mid", JobStateFailed, 20),
			},
			want: []string{"early", "mid", "late"},
		},
		{
			name: "相同StartedMS按ID字节序",
			adds: []JobSnapshot{
				snap("c", JobStateRunning, 5),
				snap("a", JobStateDone, 5),
				snap("b", JobStateFailed, 5),
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "混合StartedMS与ID并列",
			adds: []JobSnapshot{
				snap("b", JobStateDone, 20),
				snap("z", JobStateRunning, 10),
				snap("a", JobStateFailed, 20),
				snap("y", JobStateDone, 10),
			},
			want: []string{"y", "z", "a", "b"},
		},
		{
			name: "负StartedMS排在前面",
			adds: []JobSnapshot{
				snap("pos", JobStateRunning, 100),
				snap("neg", JobStateDone, -100),
				snap("zero", JobStateFailed, 0),
			},
			want: []string{"neg", "zero", "pos"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b JobBoard
			for _, s := range tc.adds {
				b.Upsert(s)
			}
			got := ids(b.List())
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("List ID = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestJobBoardStats 表驱动验证各状态的统计，含未知状态被忽略与空看板全零。
func TestJobBoardStats(t *testing.T) {
	tests := []struct {
		name        string
		adds        []JobSnapshot
		wantRunning int
		wantDone    int
		wantFailed  int
	}{
		{
			name: "空看板全零",
			adds: nil,
		},
		{
			name: "三种状态各若干",
			adds: []JobSnapshot{
				snap("r1", JobStateRunning, 1),
				snap("r2", JobStateRunning, 2),
				snap("d1", JobStateDone, 3),
				snap("f1", JobStateFailed, 4),
				snap("f2", JobStateFailed, 5),
				snap("f3", JobStateFailed, 6),
			},
			wantRunning: 2,
			wantDone:    1,
			wantFailed:  3,
		},
		{
			name: "未知状态被忽略",
			adds: []JobSnapshot{
				snap("r", JobStateRunning, 1),
				snap("x1", "queued", 2),
				snap("x2", "", 3),
				snap("x3", "RUNNING", 4), // 大小写敏感，不算 running
			},
			wantRunning: 1,
		},
		{
			name: "仅失败",
			adds: []JobSnapshot{
				snap("f1", JobStateFailed, 1),
				snap("f2", JobStateFailed, 2),
			},
			wantFailed: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b JobBoard
			for _, s := range tc.adds {
				b.Upsert(s)
			}
			run, done, failed := b.Stats()
			if run != tc.wantRunning || done != tc.wantDone || failed != tc.wantFailed {
				t.Fatalf("Stats = (%d,%d,%d), want (%d,%d,%d)",
					run, done, failed, tc.wantRunning, tc.wantDone, tc.wantFailed)
			}
		})
	}
}

// TestJobBoardCJKCommand 验证含中文与多字节字符的 Command/ID 被原样保留，
// 排序与统计不受非 ASCII 字符影响。
func TestJobBoardCJKCommand(t *testing.T) {
	var b JobBoard
	b.Upsert(JobSnapshot{
		ID:          "作业-北京",
		Command:     "编译-项目甲 && 测试",
		State:       JobStateRunning,
		StartedMS:   20,
		DurationMS:  1500,
		OutputBytes: 4096,
	})
	b.Upsert(JobSnapshot{
		ID:          "作业-上海",
		Command:     "go test ./... -run Test中文",
		State:       JobStateDone,
		StartedMS:   10,
		DurationMS:  200,
		OutputBytes: 128,
	})

	list := b.List()
	if len(list) != 2 {
		t.Fatalf("List 长度 = %d, want 2", len(list))
	}
	// StartedMS 10 的“上海”应排在 20 的“北京”之前。
	if list[0].ID != "作业-上海" || list[1].ID != "作业-北京" {
		t.Fatalf("CJK ID 排序 = %v, want [作业-上海 作业-北京]", ids(list))
	}
	if list[0].Command != "go test ./... -run Test中文" ||
		list[1].Command != "编译-项目甲 && 测试" {
		t.Fatalf("CJK Command 被破坏: %+v", list)
	}
	if run, done, failed := b.Stats(); run != 1 || done != 1 || failed != 0 {
		t.Fatalf("CJK Stats = (%d,%d,%d), want (1,1,0)", run, done, failed)
	}
}

// TestJobBoardSnapshotCopy 验证 Upsert 按值保存、List 返回副本：修改入参或
// 返回切片都不会污染看板，List 的排序也不改变底层存储。
func TestJobBoardSnapshotCopy(t *testing.T) {
	var b JobBoard

	input := JobSnapshot{
		ID:          "a",
		Command:     "原始",
		State:       JobStateRunning,
		StartedMS:   1,
		DurationMS:  10,
		OutputBytes: 5,
	}
	b.Upsert(input)

	// 修改入参，看板应保留写入时的值。
	input.Command = "被修改"
	input.State = JobStateDone
	input.StartedMS = 999
	input.OutputBytes = 999

	want := []JobSnapshot{{
		ID:          "a",
		Command:     "原始",
		State:       JobStateRunning,
		StartedMS:   1,
		DurationMS:  10,
		OutputBytes: 5,
	}}
	if got := b.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改入参后 List = %+v, want %+v", got, want)
	}

	// 修改返回切片的元素，看板应保持不变。
	got := b.List()
	got[0].Command = "污染"
	got[0].State = JobStateFailed
	if again := b.List(); !reflect.DeepEqual(again, want) {
		t.Fatalf("修改返回切片后 List = %+v, want %+v", again, want)
	}
	if run, done, failed := b.Stats(); run != 1 || done != 0 || failed != 0 {
		t.Fatalf("修改返回切片后 Stats = (%d,%d,%d), want (1,0,0)", run, done, failed)
	}
}

// TestJobBoardZeroValue 验证零值 JobBoard 可直接调用全部方法：初始 List 为
// nil、Stats 全零，Remove 安全；Upsert 后可正常读写。
func TestJobBoardZeroValue(t *testing.T) {
	var b JobBoard

	if got := b.List(); got != nil {
		t.Fatalf("零值 List = %v, want nil", got)
	}
	if run, done, failed := b.Stats(); run != 0 || done != 0 || failed != 0 {
		t.Fatalf("零值 Stats = (%d,%d,%d), want (0,0,0)", run, done, failed)
	}
	b.Remove("none")

	b.Upsert(snap("x", JobStateDone, 7))
	if got := ids(b.List()); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("零值 Upsert 后 List ID = %v, want [x]", got)
	}
	if run, done, failed := b.Stats(); run != 0 || done != 1 || failed != 0 {
		t.Fatalf("零值 Upsert 后 Stats = (%d,%d,%d), want (0,1,0)", run, done, failed)
	}
}

// TestJobBoardConcurrent 在并发 Upsert/List/Stats 下验证锁保护：写入唯一 ID，
// 最终总数可精确断言；随后并发 Remove 再断言清空，且 -race 下无数据竞争。
func TestJobBoardConcurrent(t *testing.T) {
	const (
		writers   = 8
		perWriter = 500
	)
	var b JobBoard

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				b.Upsert(JobSnapshot{
					ID:          fmt.Sprintf("job-%d-%d", w, i),
					Command:     fmt.Sprintf("cmd-%d-%d", w, i),
					State:       []string{JobStateRunning, JobStateDone, JobStateFailed}[i%3],
					StartedMS:   int64(w*perWriter + i),
					DurationMS:  int64(i % 17),
					OutputBytes: i % 32,
				})
			}
		}(w)
	}
	for r := 0; r < writers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = b.List()
				_, _, _ = b.Stats()
			}
		}()
	}
	wg.Wait()

	want := writers * perWriter
	if got := len(b.List()); got != want {
		t.Fatalf("并发写入后 List 长度 = %d, want %d", got, want)
	}
	run, done, failed := b.Stats()
	if run+done+failed != want {
		t.Fatalf("并发写入后 Stats 总和 = %d, want %d", run+done+failed, want)
	}

	// 并发删除全部条目，最终应清空。
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				b.Remove(fmt.Sprintf("job-%d-%d", w, i))
			}
		}(w)
	}
	wg.Wait()

	if got := b.List(); got != nil {
		t.Fatalf("并发删除后 List = %v（长度 %d）, want nil", got, len(got))
	}
	if run, done, failed := b.Stats(); run != 0 || done != 0 || failed != 0 {
		t.Fatalf("并发删除后 Stats = (%d,%d,%d), want (0,0,0)", run, done, failed)
	}
}
