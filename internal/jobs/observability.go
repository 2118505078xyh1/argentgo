package jobs

import (
	"sort"
	"sync"
)

// 作业看板快照使用的状态取值。它们与 Registry 的 Status 相互独立，
// 只用于统计聚合，不参与任何现有作业的生命周期。
const (
	JobStateRunning = "running"
	JobStateDone    = "done"
	JobStateFailed  = "failed"
)

// JobSnapshot 是单个后台作业的诊断快照。全部为值字段，便于按值复制与
// 并发共享，不涉及任何 I/O 或外部资源。
//
// StartedMS 记录作业启动时刻（毫秒），仅用于 List 的稳定排序，不参与
// Stats 聚合。DurationMS 为执行耗时（毫秒），OutputBytes 为输出字节数。
type JobSnapshot struct {
	ID          string
	Command     string
	State       string
	StartedMS   int64
	DurationMS  int64
	OutputBytes int
}

// JobBoard 保存当前在册的后台作业快照：一个带锁的 map[ID]JobSnapshot。
// 所有方法可被多个 goroutine 并发调用，零值 JobBoard 可直接使用。
//
// JobBoard 是零 I/O、零调用方的可观测辅助：不接入 Registry，也不改变任何
// 现有作业行为。用途是为未来的 /v1/status 汇聚后台作业状态预留诊断原语，
// 当前不接线（SetJobs 尚未在生产启用）。
type JobBoard struct {
	mu   sync.RWMutex
	jobs map[string]JobSnapshot
}

// Upsert 按 ID 写入或覆盖一条快照。s 按值保存，调用方后续修改 s 不会影响看板。
func (b *JobBoard) Upsert(s JobSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 兼容未显式初始化的零值看板。
	if b.jobs == nil {
		b.jobs = make(map[string]JobSnapshot)
	}
	b.jobs[s.ID] = s
}

// Remove 删除指定 ID 的快照；ID 不存在（或看板为空）时什么也不做。
func (b *JobBoard) Remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 对 nil map 执行 delete 是安全的。
	delete(b.jobs, id)
}

// List 返回全部快照的副本，按 StartedMS 升序、ID 字节序稳定排列。
// 返回值与内部 map 不共享数据：调用方修改副本不影响看板。空看板返回 nil。
func (b *JobBoard) List() []JobSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.jobs) == 0 {
		return nil
	}
	out := make([]JobSnapshot, 0, len(b.jobs))
	for _, s := range b.jobs {
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if a.StartedMS != c.StartedMS {
			return a.StartedMS < c.StartedMS
		}
		return a.ID < c.ID
	})
	return out
}

// Stats 在单个读锁内聚合各状态的作业数：running/done/failed 分别对应
// JobStateRunning/JobStateDone/JobStateFailed，其它状态被忽略。空看板返回全零。
func (b *JobBoard) Stats() (running, done, failed int) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, s := range b.jobs {
		switch s.State {
		case JobStateRunning:
			running++
		case JobStateDone:
			done++
		case JobStateFailed:
			failed++
		}
	}
	return running, done, failed
}
