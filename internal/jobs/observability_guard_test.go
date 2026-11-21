package jobs

import (
	"bytes"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// guardSnap 构造一条字段完全由参数决定的快照。
// 独立命名以免与既有 observability_test.go 中的 snap 辅助函数重名。
func guardSnap(id, command, state string, startedMS, durationMS int64, outputBytes int) JobSnapshot {
	return JobSnapshot{
		ID:          id,
		Command:     command,
		State:       state,
		StartedMS:   startedMS,
		DurationMS:  durationMS,
		OutputBytes: outputBytes,
	}
}

// guardIDs 提取快照 ID 序列，保留 nil 语义。
func guardIDs(list []JobSnapshot) []string {
	if list == nil {
		return nil
	}
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.ID
	}
	return out
}

// guardRefList 按与 JobBoard.List 相同的规则（StartedMS 升序、ID 字节序）
// 对参考 map 排序，供差分对拍使用。
func guardRefList(ref map[string]JobSnapshot) []JobSnapshot {
	if len(ref) == 0 {
		return nil
	}
	out := make([]JobSnapshot, 0, len(ref))
	for _, s := range ref {
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedMS != out[j].StartedMS {
			return out[i].StartedMS < out[j].StartedMS
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// guardAssertMatches 对拍看板与独立参考 map：List 必须逐字段一致，
// Stats 必须与参考 map 的聚合结果一致。
func guardAssertMatches(t *testing.T, b *JobBoard, ref map[string]JobSnapshot) {
	t.Helper()
	want := guardRefList(ref)
	if got := b.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("List 与参考 map 不一致:\n got=%+v\nwant=%+v", got, want)
	}

	var wantRunning, wantDone, wantFailed int
	for _, s := range ref {
		switch s.State {
		case JobStateRunning:
			wantRunning++
		case JobStateDone:
			wantDone++
		case JobStateFailed:
			wantFailed++
		}
	}
	if running, done, failed := b.Stats(); running != wantRunning || done != wantDone || failed != wantFailed {
		t.Fatalf("Stats=(%d,%d,%d), want (%d,%d,%d)",
			running, done, failed, wantRunning, wantDone, wantFailed)
	}
}

// TestJobBoardDifferentialGuard 用固定种子的伪随机序列驱动 Upsert/Remove，
// 每步都与独立参考 map 对拍 List 与 Stats，守护覆盖/删除/未知状态/并列排序。
func TestJobBoardDifferentialGuard(t *testing.T) {
	const (
		ops      = 1200
		keySpace = 32
	)
	rng := rand.New(rand.NewSource(20261121))
	states := []string{JobStateRunning, JobStateDone, JobStateFailed, "queued", ""}
	commands := []string{"编译-项目甲", "go test ./... -run Test中文", "cargo build", "🚀 发布", ""}

	var b JobBoard
	ref := make(map[string]JobSnapshot, keySpace)

	for n := 0; n < ops; n++ {
		key := fmt.Sprintf("job-%02d", rng.Intn(keySpace))
		if rng.Intn(10) < 4 {
			b.Remove(key)
			delete(ref, key)
		} else {
			s := guardSnap(
				key,
				commands[rng.Intn(len(commands))],
				states[rng.Intn(len(states))],
				int64(rng.Intn(9)-4),      // 含并列与负数，考验 StartedMS 排序
				int64(rng.Intn(5000)-100), // 耗时按值保存，允许负数
				rng.Intn(1<<16),           // 输出字节数
			)
			b.Upsert(s)
			ref[s.ID] = s
		}
		guardAssertMatches(t, &b, ref)
	}
}

// TestJobBoardSnapshotIsolationGuard 验证三重置换隔离：修改 Upsert 入参、
// 修改 List 返回副本、对返回切片 append，均不得影响看板内部或其它副本。
func TestJobBoardSnapshotIsolationGuard(t *testing.T) {
	var b JobBoard
	input := guardSnap("键-A", "命令-原始", JobStateRunning, 10, 20, 30)
	b.Upsert(input)

	want := []JobSnapshot{guardSnap("键-A", "命令-原始", JobStateRunning, 10, 20, 30)}

	// 修改入参不应污染看板。
	input.ID = "键-B"
	input.Command = "命令-修改"
	input.State = JobStateFailed
	input.StartedMS = 999
	input.DurationMS = 999
	input.OutputBytes = 999
	if got := b.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改入参后 List=%+v, want %+v", got, want)
	}

	// 两次 List 返回彼此独立、且与内部存储独立的副本。
	mutated := b.List()
	untouched := b.List()
	mutated[0].Command = "污染-命令"
	mutated[0].State = JobStateDone
	mutated[0].OutputBytes = -1
	mutated = append(mutated, guardSnap("额外", "额外命令", JobStateRunning, 0, 0, 0))

	if !reflect.DeepEqual(untouched, want) {
		t.Fatalf("修改一个返回副本影响了另一个副本: got=%+v, want=%+v", untouched, want)
	}
	if got := b.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("修改返回副本影响了内部存储: got=%+v, want=%+v", got, want)
	}
	if running, done, failed := b.Stats(); running != 1 || done != 0 || failed != 0 {
		t.Fatalf("污染副本后 Stats=(%d,%d,%d), want (1,0,0)", running, done, failed)
	}
}

// TestJobBoardCJKByteGuard 验证中文/多字节/非法 UTF-8 字节的 Command 与 ID
// 逐字节原样保留，且字节序排序与参考 sort.Strings 一致。
func TestJobBoardCJKByteGuard(t *testing.T) {
	cases := []struct{ id, command string }{
		{"作业-北京", "编译-项目甲 && 测试"},
		{"job-日本語", "go test ./... -run Test中文"},
		{"emoji-🚀-发布", "🚀 🚀 🚀"},
		{"全角-ＡＢＣ", "＋－×÷ ≠ ≤ ≥"},
		{"combining-e\u0301", "café naïve"},
		{"raw-bytes", "前缀\xff\xfe后缀"},
	}

	var b JobBoard
	for i, tc := range cases {
		b.Upsert(guardSnap(tc.id, tc.command, JobStateRunning, int64(len(cases)-i), int64(i), i))
	}

	list := b.List()
	if len(list) != len(cases) {
		t.Fatalf("List 长度=%d, want %d", len(list), len(cases))
	}
	// StartedMS=len(cases)-i，升序即 cases 的逆序。
	for i, tc := range cases {
		got := list[len(cases)-1-i]
		if !bytes.Equal([]byte(got.ID), []byte(tc.id)) || len(got.ID) != len(tc.id) {
			t.Fatalf("ID 字节被破坏: got=%q, want=%q", got.ID, tc.id)
		}
		if !bytes.Equal([]byte(got.Command), []byte(tc.command)) || len(got.Command) != len(tc.command) {
			t.Fatalf("Command 字节被破坏: got=%q, want=%q", got.Command, tc.command)
		}
	}

	// StartedMS 全部相同时，ID 必须按字节序排列（与 sort.Strings 等价）。
	var tie JobBoard
	wantOrder := make([]string, len(cases))
	for i, tc := range cases {
		tie.Upsert(guardSnap(tc.id, tc.command, JobStateDone, 7, 0, 0))
		wantOrder[i] = tc.id
	}
	sort.Strings(wantOrder)
	if got := guardIDs(tie.List()); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("相同 StartedMS 的 ID 字节序=%q, want %q", got, wantOrder)
	}
}

// guardConcurrentSnap 让写 goroutine 与最终期望值共用同一构造逻辑，
// 避免预计算期望快照时与实际最后一次写入产生偏差。
func guardConcurrentSnap(w, i int, states []string) JobSnapshot {
	id := fmt.Sprintf("worker-%02d", w)
	return guardSnap(id, fmt.Sprintf("命令-%02d", w), states[(i+w)%len(states)], int64(i-w), int64(w), i)
}

// TestJobBoardConcurrentGuard 以 16 个 goroutine × 300 次操作混合
// Upsert/Remove/List/Stats：写者各用独立 ID，读者只做单次调用内的一致性校验，
// 结束后与预计算的最终状态精确对拍，-race 下不得出现数据竞争。
func TestJobBoardConcurrentGuard(t *testing.T) {
	const (
		workers    = 16
		iterations = 300
	)
	// 并发测试只用已知状态，便于单调用内校验 Stats 与 List 的关系。
	states := []string{JobStateRunning, JobStateDone, JobStateFailed}
	var b JobBoard

	// 预计算每个写者最后一次 Upsert 后的状态（i=iterations-1 不是 Remove 步）。
	final := make(map[string]JobSnapshot)
	for w := 0; w < workers; w++ {
		if w%4 == 3 {
			continue
		}
		s := guardConcurrentSnap(w, iterations-1, states)
		final[s.ID] = s
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			if w%4 == 3 {
				for i := 0; i < iterations; i++ {
					list := b.List()
					for k := 1; k < len(list); k++ {
						a, c := list[k-1], list[k]
						if a.StartedMS > c.StartedMS || (a.StartedMS == c.StartedMS && a.ID >= c.ID) {
							t.Errorf("并发 List 未按要求排序: %+v", list)
							return
						}
					}
					running, done, failed := b.Stats()
					if running < 0 || done < 0 || failed < 0 {
						t.Errorf("并发 Stats 出现负值: (%d,%d,%d)", running, done, failed)
						return
					}
				}
				return
			}

			id := fmt.Sprintf("worker-%02d", w)
			for i := 0; i < iterations; i++ {
				if i%7 == 0 {
					b.Remove(id)
					continue
				}
				b.Upsert(guardConcurrentSnap(w, i, states))
			}
		}(w)
	}
	wg.Wait()

	guardAssertMatches(t, &b, final)
}

// TestJobBoardZeroValueGuard 验证零值与 new(JobBoard) 的两种空看板：
// List 为 nil、Stats 全零、Remove 安全，Upsert 后可读写、删空后回到 nil。
func TestJobBoardZeroValueGuard(t *testing.T) {
	var zero JobBoard
	boards := []*JobBoard{&zero, new(JobBoard)}
	wantSnap := guardSnap("x", "命令", JobStateDone, 5, 0, 0)

	for _, b := range boards {
		if got := b.List(); got != nil {
			t.Fatalf("零值 List=%v, want nil", got)
		}
		if running, done, failed := b.Stats(); running != 0 || done != 0 || failed != 0 {
			t.Fatalf("零值 Stats=(%d,%d,%d), want (0,0,0)", running, done, failed)
		}
		b.Remove("不存在")
		if got := b.List(); got != nil {
			t.Fatalf("空看板 Remove 后 List=%v, want nil", got)
		}

		b.Upsert(wantSnap)
		guardAssertMatches(t, b, map[string]JobSnapshot{"x": wantSnap})

		b.Remove("x")
		if got := b.List(); got != nil {
			t.Fatalf("删空后 List=%v, want nil", got)
		}
		if running, done, failed := b.Stats(); running != 0 || done != 0 || failed != 0 {
			t.Fatalf("删空后 Stats=(%d,%d,%d), want (0,0,0)", running, done, failed)
		}
	}
}

// TestJobBoardSortStabilityGuard 验证排序与插入顺序无关、重复调用稳定，
// 相同 StartedMS 按 ID 字节序排列，覆盖已有 ID 改变 StartedMS 后会重新排序。
func TestJobBoardSortStabilityGuard(t *testing.T) {
	items := []JobSnapshot{
		guardSnap("作业-乙", "cmd", JobStateRunning, 5, 0, 0),
		guardSnap("job-z", "cmd", JobStateDone, 5, 0, 0),
		guardSnap("作业-甲", "cmd", JobStateFailed, -1, 0, 0),
		guardSnap("job-a", "cmd", JobStateRunning, 5, 0, 0),
		guardSnap("job-m", "cmd", JobStateDone, 5, 0, 0),
	}
	ref := make(map[string]JobSnapshot, len(items))
	for _, s := range items {
		ref[s.ID] = s
	}
	want := guardRefList(ref)

	var forward, backward JobBoard
	for _, s := range items {
		forward.Upsert(s)
	}
	for i := len(items) - 1; i >= 0; i-- {
		backward.Upsert(items[i])
	}
	if got := forward.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("正序插入 List=%+v, want %+v", got, want)
	}
	if got := backward.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("逆序插入 List=%+v, want %+v", got, want)
	}
	for i := 0; i < 3; i++ {
		if got := forward.List(); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次重复 List=%+v, want %+v", i+1, got, want)
		}
	}
	for i := 1; i < len(want); i++ {
		a, c := want[i-1], want[i]
		if a.StartedMS > c.StartedMS || (a.StartedMS == c.StartedMS && a.ID >= c.ID) {
			t.Fatalf("期望序列本身不满足 StartedMS/ID 排序: %+v", want)
		}
	}

	// 覆盖 job-a 的 StartedMS 为最小值，它应跃升到首位。
	updated := guardSnap("job-a", "新的命令", JobStateFailed, -99, 12, 34)
	forward.Upsert(updated)
	ref[updated.ID] = updated
	guardAssertMatches(t, &forward, ref)
	if got := forward.List()[0]; got.ID != "job-a" || got.Command != "新的命令" {
		t.Fatalf("覆盖后首条=%+v, want job-a/新的命令", got)
	}
}
