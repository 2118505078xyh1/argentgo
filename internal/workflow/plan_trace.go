package workflow

import "sync"

// TraceEntry 记录某一步在某个时刻进入的相位。
// Step 是步骤名，Phase 是相位名（如 "ready"/"running"/"done"），At 是时间戳。
// 三个字段都是值语义，TraceEntry 本身是可安全拷贝的纯值对象。
type TraceEntry struct {
	Step  string
	Phase string
	At    int64
}

// PlanTrace 是一个线程安全的执行轨迹累加器：Mark 追加一条相位变化，
// Snapshot 返回当前全部条目的副本，StepsInPhase 按首次出现顺序返回某相位下
// 去重后的步骤名。
//
// 设计约束：
//   - 零 I/O、零时钟、零随机性；At 完全由调用方传入。
//   - 纯状态容器，没有任何调用方，不参与 Run/RunGuarded 执行路径。
//   - mu 保护 Entries 的所有读写。请勿在首次使用后拷贝 PlanTrace（内含锁）；
//     也不要在锁外直接读写导出的 Entries，并发场景一律使用方法。
//   - 零值可用：var t PlanTrace 后可直接 Mark/Snapshot/StepsInPhase。
type PlanTrace struct {
	mu      sync.RWMutex
	Entries []TraceEntry
}

// NewPlanTrace 构造一个空的 PlanTrace。Entries 保持 nil，首次 Mark 时再分配，
// 因此 Snapshot/StepsInPhase 对空轨迹返回 nil。
func NewPlanTrace() *PlanTrace {
	return &PlanTrace{}
}

// Mark 把 (step, phase, at) 作为一条新记录追加到轨迹末尾，不做去重、不排序、
// 不修改入参（三个入参都是值类型）。同一 (step, phase) 可被多次记录，
// 每次都会产生独立条目。nil 接收者是安全的空操作。
func (t *PlanTrace) Mark(step, phase string, at int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Entries = append(t.Entries, TraceEntry{Step: step, Phase: phase, At: at})
}

// Snapshot 返回轨迹的独立副本，条目顺序与 Mark 的追加顺序一致。
// 返回值与内部切片零共享：修改返回的切片不会影响后续 Snapshot 或轨迹本身，
// 之后新增的条目也不会出现在此前取得的快照里。无条目时返回 nil。
// nil 接收者返回 nil。
func (t *PlanTrace) Snapshot() []TraceEntry {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]TraceEntry(nil), t.Entries...)
}

// StepsInPhase 返回进入 phase 的步骤名，按它们在轨迹中首次出现的顺序去重：
// 同一步骤即便多次记录该相位，也只保留第一次出现的位置；不同步骤之间保持
// 稳定顺序。返回值是新切片，修改它不会影响轨迹。没有匹配条目时返回 nil。
// nil 接收者返回 nil。
func (t *PlanTrace) StepsInPhase(phase string) []string {
	entries := t.Snapshot()
	seen := make(map[string]bool)
	var steps []string
	for _, entry := range entries {
		if entry.Phase != phase || seen[entry.Step] {
			continue
		}
		seen[entry.Step] = true
		steps = append(steps, entry.Step)
	}
	return steps
}
