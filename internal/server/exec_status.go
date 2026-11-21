package server

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// 本文件提供一个纯内存的「shell 执行状态看板」。它把若干次 shell 执行的
// 生命周期快照（排队/运行/完成/失败/取消）汇总到一个带锁的映射里，便于上层
// 在任意时刻查询与渲染。它不注册路由、不读写磁盘、不读全局状态、不联系任何
// 服务，默认零调用方。

// ExecPhase 表示一次 shell 执行所处的阶段。
type ExecPhase string

// 执行阶段的取值。字符串值与外部呈现保持一致，便于直接序列化或比较。
const (
	ExecQueued    ExecPhase = "queued"
	ExecRunning   ExecPhase = "running"
	ExecCompleted ExecPhase = "completed"
	ExecFailed    ExecPhase = "failed"
	ExecCancelled ExecPhase = "cancelled"
)

// ExecStatus 是一次 shell 执行的不可变状态快照。时间戳以 Unix 毫秒计，
// EndedMS 与 ExitCode 仅在终态（完成/失败/取消）时有意义。
type ExecStatus struct {
	ID        string
	Phase     ExecPhase
	Command   string
	StartedMS int64
	EndedMS   int64
	ExitCode  int
}

// ExecStatusBoard 按 ID 保存执行状态。所有方法都可安全并发调用。
//
// 零值即可用：entries 延迟初始化，因此 var b ExecStatusBoard 与
// &ExecStatusBoard{} 行为一致，默认空。
type ExecStatusBoard struct {
	mu      sync.RWMutex
	entries map[string]ExecStatus
}

// Upsert 插入或覆盖 s.ID 对应的状态。s 按值保存，调用方后续修改 s 不会影响看板。
func (b *ExecStatusBoard) Upsert(s ExecStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = make(map[string]ExecStatus)
	}
	b.entries[s.ID] = s
}

// Get 返回 id 对应的状态副本。不存在时返回零值与 false。
func (b *ExecStatusBoard) Get(id string) (ExecStatus, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	s, ok := b.entries[id]
	return s, ok
}

// List 返回当前所有状态的副本切片，按 StartedMS 升序排列；StartedMS 相同时
// 以 ID 字典序作为稳定的 tie-break，因此相同状态总得到相同顺序。返回值是新
// 切片，调用方修改它不会影响看板。零值看板返回非 nil 的空切片。
func (b *ExecStatusBoard) List() []ExecStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]ExecStatus, 0, len(b.entries))
	for _, s := range b.entries {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedMS != out[j].StartedMS {
			return out[i].StartedMS < out[j].StartedMS
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Remove 删除 id 对应的状态。id 不存在或看板为零值时均为空操作。
func (b *ExecStatusBoard) Remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		return
	}
	delete(b.entries, id)
}

// execPhaseLabel 把执行阶段映射为汇总用的中文标签。未知阶段返回空字符串，
// 由 Summary 以「未知(...)」的形式单独呈现。
func execPhaseLabel(p ExecPhase) string {
	switch p {
	case ExecQueued:
		return "排队"
	case ExecRunning:
		return "运行中"
	case ExecCompleted:
		return "已完成"
	case ExecFailed:
		return "失败"
	case ExecCancelled:
		return "已取消"
	default:
		return ""
	}
}

// Summary 返回一份稳定的中文汇总：按固定阶段顺序给出计数，未知阶段按阶段
// 字符串字典序追加在末尾，最后给出合计。稳定性体现在：
//   - 顺序固定，与 map 迭代及插入顺序无关；
//   - 未知阶段排序输出，避免随机顺序；
//   - 只做整数与字符串处理，与 CJK、区域设置无关。
//
// 零值看板输出各阶段为 0、合计 0 的汇总。该方法不修改接收者。
func (b *ExecStatusBoard) Summary() string {
	counts := make(map[ExecPhase]int)
	b.mu.RLock()
	for _, s := range b.entries {
		counts[s.Phase]++
	}
	b.mu.RUnlock()

	// 固定顺序遍历已知阶段，保证输出与插入顺序无关。
	known := []ExecPhase{ExecQueued, ExecRunning, ExecCompleted, ExecFailed, ExecCancelled}
	parts := make([]string, 0, len(counts)+1)
	total := 0
	for _, p := range known {
		n := counts[p]
		total += n
		parts = append(parts, execPhaseLabel(p)+" "+strconv.Itoa(n))
		delete(counts, p)
	}

	// 未知阶段按阶段字符串排序后追加，同样为了输出确定。
	unknown := make([]string, 0, len(counts))
	for p := range counts {
		unknown = append(unknown, string(p))
	}
	sort.Strings(unknown)
	for _, raw := range unknown {
		n := counts[ExecPhase(raw)]
		total += n
		parts = append(parts, "未知("+raw+") "+strconv.Itoa(n))
	}

	return "执行状态：" + strings.Join(parts, "，") + "；合计 " + strconv.Itoa(total)
}
