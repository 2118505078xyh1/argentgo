package agent

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// 本文件提供 ToolCallTrace：在一个 turn 内按调用顺序记录工具调用的跨度链，
// 每项含顺序号、工具名、耗时、成败与错误文本，并给出中文一行摘要。
// 与 turn_timing / tool_timing_histogram 一样，它是不依赖外部状态的纯原语：
// 不读写全局量（时钟除外）、不做 I/O、不接入实际工具执行。接线层在需要时
// 再决定何时 Begin/End 以及落库方式。

// TraceSpan 是一次工具调用的跨度快照。所有字段都是值类型，调用方持有后
// 不会被内部后续写入改变。
type TraceSpan struct {
	Seq        int    // 调用顺序号，从 0 开始；决定 Snapshot 中的链顺序
	Tool       string // 工具名（可含 CJK 字符，原样保留）
	DurationMs int64  // 耗时毫秒，钳制后 >= 0
	Ok         bool   // 工具调用是否成功
	Error      string // 错误文本；err 为 nil 时为空串（与 Ok 无关）
}

// pendingSpan 是 Begin 与 End 之间的中间状态，仅存在于内部映射中。
type pendingSpan struct {
	seq   int
	tool  string
	start time.Time
}

// ToolCallTrace 按调用顺序累积工具调用跨度。
//
// 字段语义：
//   - pending：已 Begin 尚未 End 的跨度，以 spanID 为键；End 配对后移除。
//   - spans：已结算的跨度；Snapshot/Summary 只读取它们。
//   - nextSeq：单调分配的调用顺序号，同时用作 spanID，保证二者一一对应。
//   - now：可注入时钟，nil 时用 time.Now；仅供测试确定性计时，生产无需设置。
//
// 零值即可使用：mu 是值字段，pending 为 nil 时 Begin 惰性 make，读方法对 nil
// 切片安全。所有方法使用指针接收者并共用 mu；本类型从不按值复制，因此不存在
// copylocks 风险，可被多 goroutine 并发调用。
type ToolCallTrace struct {
	mu      sync.Mutex
	now     func() time.Time
	nextSeq int
	pending map[int]pendingSpan
	spans   []TraceSpan
}

// nowTime 返回当前时刻。now 字段在首次使用前设置，读取始终在锁内。
func (t *ToolCallTrace) nowTime() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// Begin 开启一次工具调用，返回 spanID 供 End 配对。
// 顺序号在 Begin 时按调用先后单调分配，因此即使工具并发执行、End 乱序到达，
// Snapshot 仍按调用发起顺序还原整条链。未知工具名为空串时同样被记录。
func (t *ToolCallTrace) Begin(tool string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.pending == nil {
		t.pending = make(map[int]pendingSpan)
	}
	id := t.nextSeq
	t.nextSeq++
	t.pending[id] = pendingSpan{seq: id, tool: tool, start: t.nowTime()}
	return id
}

// End 结算 spanID 对应的跨度，记录耗时、成败与错误文本。
// 耗时由起止时刻差取毫秒；系统时钟回拨导致的负值被钳制为 0，绝不产生负耗时。
// 未知或重复的 spanID 是 no-op，因此成对的 Begin/End 可放心调用；孤立 End
// 不会污染统计。err 为 nil 时 Error 记为空串，否则取 err.Error()。
func (t *ToolCallTrace) End(spanID int, ok bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	p, found := t.pending[spanID]
	if !found {
		return
	}
	delete(t.pending, spanID)
	duration := t.nowTime().Sub(p.start).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	t.spans = append(t.spans, TraceSpan{
		Seq:        p.seq,
		Tool:       p.tool,
		DurationMs: duration,
		Ok:         ok,
		Error:      errText,
	})
}

// Snapshot 返回按 Seq 升序排列的跨度副本。先复制再排序，绝不改动内部切片，
// 也不保留对内部的引用；调用方随意修改返回值都不会影响原对象。
func (t *ToolCallTrace) Snapshot() []TraceSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TraceSpan, len(t.spans))
	copy(out, t.spans)
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Summary 返回一行中文摘要：N次调用/M次失败/总耗时Xms。
// 失败数以 Ok=false 计数，总耗时为所有跨度耗时之和（均已钳制为非负）。
// 零值返回 "0次调用/0次失败/总耗时0ms"。
func (t *ToolCallTrace) Summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total int64
	fails := 0
	for _, s := range t.spans {
		total += s.DurationMs
		if !s.Ok {
			fails++
		}
	}
	return fmt.Sprintf("%d次调用/%d次失败/总耗时%dms", len(t.spans), fails, total)
}
