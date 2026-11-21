package agent

import (
	"sync"
	"time"
)

// 本文件提供 TurnTiming：把一轮 agent 运行的耗时按阶段（推理/工具/压缩/等待用户/开销）
// 拆开记账，并把结论固化为可断言的字段（complete / incompleteReasons）。
// 它刻意保持为不依赖外部状态的纯原语：不读写时钟以外的全局量、不做 I/O、不接入 loop。
// 接线层（internal/app 或其他会话）在需要时再决定采样率（如 1%）与落库方式。

// TurnPhase 是一轮运行中被单独计时的阶段。
type TurnPhase string

const (
	phaseInference  TurnPhase = "inference"  // 等待模型推理/流式输出
	phaseTools      TurnPhase = "tools"      // 执行工具调用
	phaseCompaction TurnPhase = "compaction" // 上下文压缩
	phaseUserWait   TurnPhase = "user_wait"  // 等待用户输入/审批
	phaseOverhead   TurnPhase = "overhead"   // 调度、记账等其他开销
)

// turnTimingInitMu 保护 TurnTiming 零值的惰性初始化：多个 goroutine 首次并发使用同一个
// 零值时，由它串行化 mu / phases / starts 字段的创建，避免首次写入互相竞争。
// 一旦初始化完成，后续并发读写都只经实例自身的 mu，不再触碰这把全局锁。
var turnTimingInitMu sync.Mutex

// TurnTiming 记录一轮运行中各阶段的累计耗时与完成状态。
//
// 字段语义：
//   - phases：每个阶段累计的耗时（同一阶段多次 Begin/End 会累加）。
//   - starts：正在计时的阶段开始时刻；EndPhase 结算后移除。
//   - complete：是否完整结束（MarkComplete 置真，MarkIncomplete 置假）。
//   - incompleteReasons：未完整结束的原因列表（MarkIncomplete 逐条追加）。
//
// 零值即可使用：mu 是指针而非值，因此值接收者 Total 不会触发 vet 的 copylocks 警告，
// 但首次使用前由 ensure 惰性分配 mu 与内部 map。所有方法均可被多 goroutine 并发调用。
type TurnTiming struct {
	mu                *sync.Mutex
	phases            map[TurnPhase]time.Duration
	starts            map[TurnPhase]time.Time
	complete          bool
	incompleteReasons []string
}

// ensure 惰性创建零值所需的内部状态，返回实例互斥锁。
// 初始化本身由 turnTimingInitMu 串行化，保证并发首次使用不会竞争。
func (t *TurnTiming) ensure() *sync.Mutex {
	turnTimingInitMu.Lock()
	defer turnTimingInitMu.Unlock()
	if t.mu == nil {
		t.mu = &sync.Mutex{}
	}
	if t.phases == nil {
		t.phases = make(map[TurnPhase]time.Duration)
	}
	if t.starts == nil {
		t.starts = make(map[TurnPhase]time.Time)
	}
	return t.mu
}

// BeginPhase 记录某阶段的开始时刻。重复开始同一阶段会覆盖上一次的开始时刻；
// 这使调用方即使误重复调用，也不会把同一段时间累计两次。
func (t *TurnTiming) BeginPhase(p TurnPhase) {
	mu := t.ensure()
	mu.Lock()
	defer mu.Unlock()
	t.starts[p] = time.Now()
}

// EndPhase 结算某阶段的耗时并累加到 phases。从未开始（或已被结算）的阶段是 no-op，
// 因此成对的 Begin/End 可以放心调用，无需担心孤立 End 污染统计。
func (t *TurnTiming) EndPhase(p TurnPhase) {
	mu := t.ensure()
	mu.Lock()
	defer mu.Unlock()
	start, ok := t.starts[p]
	if !ok {
		return
	}
	delete(t.starts, p)
	t.phases[p] += time.Since(start)
}

// MarkComplete 把整轮标记为完整结束。
func (t *TurnTiming) MarkComplete() {
	mu := t.ensure()
	mu.Lock()
	defer mu.Unlock()
	t.complete = true
}

// MarkIncomplete 把整轮标记为未完整结束，并追加一条原因。
// 空原因同样会被记录，便于调用方保留“曾发生不完整事件”的信号。
func (t *TurnTiming) MarkIncomplete(reason string) {
	mu := t.ensure()
	mu.Lock()
	defer mu.Unlock()
	t.complete = false
	t.incompleteReasons = append(t.incompleteReasons, reason)
}

// Total 返回所有阶段累计耗时之和。
//
// 这里使用指针接收者（而非草图里的值接收者）：值接收者会在调用瞬间按值复制整个结构体，
// 该复制发生在获取互斥锁之前，若此时别的 goroutine 正在 MarkIncomplete 等方法中改写
// complete/incompleteReasons，就会被竞态检测器判定为数据竞争。指针接收者只复制指针，
// 配合内部锁即可保证与所有写方法并发调用也安全。零值返回 0。
func (t *TurnTiming) Total() time.Duration {
	// 读取 mu 指针字段需要与 ensure 的写入同步；零值时 mu 为 nil，直接返回 0。
	turnTimingInitMu.Lock()
	mu := t.mu
	turnTimingInitMu.Unlock()
	if mu == nil {
		return 0
	}
	mu.Lock()
	defer mu.Unlock()
	var total time.Duration
	for _, d := range t.phases {
		total += d
	}
	return total
}

// Snapshot 返回当前状态的独立副本：phases/starts 两个 map 与 incompleteReasons 切片都被深拷贝。
// 调用方可以随意修改副本（含调用其 Total），不会影响原对象；副本持有独立的互斥锁。
func (t *TurnTiming) Snapshot() TurnTiming {
	mu := t.ensure()
	mu.Lock()
	defer mu.Unlock()

	snap := TurnTiming{
		mu:       &sync.Mutex{},
		complete: t.complete,
	}
	snap.phases = make(map[TurnPhase]time.Duration, len(t.phases))
	for p, d := range t.phases {
		snap.phases[p] = d
	}
	snap.starts = make(map[TurnPhase]time.Time, len(t.starts))
	for p, at := range t.starts {
		snap.starts[p] = at
	}
	if t.incompleteReasons != nil {
		snap.incompleteReasons = append([]string(nil), t.incompleteReasons...)
	}
	return snap
}
