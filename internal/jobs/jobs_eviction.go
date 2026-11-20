package jobs

import "sort"

// JobInfo 是淘汰决策所需的最小作业视图。它是 Job 的别名，
// 因此 List 的返回值可以直接传给 selectEvictionCandidates。
type JobInfo = Job

// selectEvictionCandidates 在作业数超过 max 时挑选可淘汰的作业 ID。
//
// 选择规则（确定性，与输入顺序无关）：
//   - 优先已结束的作业（exited/killed），其次才考虑仍在运行的作业；
//   - 同一类内按启动时间从早到晚（LRU）排列；
//   - 时间相同则按 ID 字节序排列，保证结果稳定且对 CJK 名称安全。
//
// 返回的 ID 数量恰好等于超出上限的数量（被 len(jobs) 截断）。
// 作业数不超过 max（或 jobs 为空）时返回 nil。本函数不修改入参。
func selectEvictionCandidates(jobs []JobInfo, max int) []string {
	over := len(jobs) - max
	if over <= 0 {
		return nil
	}
	if over > len(jobs) {
		over = len(jobs)
	}

	// 复制后排序，保持纯函数语义：绝不修改调用方的切片或元素。
	candidates := make([]JobInfo, len(jobs))
	copy(candidates, jobs)
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		ra, rb := evictionRank(a.Status), evictionRank(b.Status)
		if ra != rb {
			return ra < rb
		}
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		return a.ID < b.ID
	})

	ids := make([]string, over)
	for i := 0; i < over; i++ {
		ids[i] = candidates[i].ID
	}
	return ids
}

// evictionRank 把状态映射为淘汰优先级：终态作业排在前面。
func evictionRank(status Status) int {
	if status == StatusRunning {
		return 1
	}
	return 0
}

// SetMaxJobs 设置 registry 保留的作业数量上限。max<=0 表示不限制（这也是
// 默认值），开启后每次 Start 结束都会尝试淘汰多余作业；也可以调用
// EnforceMaxJobs 手动触发。上限默认关闭，因此不会改变既有行为。
func (r *Registry) SetMaxJobs(max int) {
	r.mu.Lock()
	r.maxJobs = max
	r.mu.Unlock()
}

// EnforceMaxJobs 在开启上限且作业数超限时淘汰多余的作业，返回被淘汰的 ID。
// 未开启上限（maxJobs<=0）或未超限时返回 nil 且不改变任何作业。
//
// 淘汰复用 Remove：它负责持久化、给仍在运行的子进程发信号并删除日志。
// 单个淘汰失败被忽略，以免影响其余作业或触发它的 Start。
func (r *Registry) EnforceMaxJobs() []string {
	r.mu.Lock()
	max := r.maxJobs
	if max <= 0 {
		r.mu.Unlock()
		return nil
	}
	infos := make([]JobInfo, 0, len(r.jobs))
	for _, job := range r.jobs {
		infos = append(infos, *job)
	}
	ids := selectEvictionCandidates(infos, max)
	r.mu.Unlock()

	for _, id := range ids {
		_ = r.Remove(id)
	}
	return ids
}
