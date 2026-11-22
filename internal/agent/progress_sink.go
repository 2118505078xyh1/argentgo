package agent

import (
	"strings"
	"sync"
)

// 本文件实现"轮次内进度检查点"的写侧。ProgressSink 由 app 注入（agent 不依赖
// session），每次累积到一定字节数就把当前已流式输出文本与工具调用交给它持久化，
// 使进程被强杀（kill -9，走不到任何失败边界）后仍有可恢复的中间进度。
//
// 写侧严格 opt-in：sink 为 nil 时所有路径是空操作，逐字节不变。

const (
	// progressByteLimit 是单个进度点保留的最大文本字节数，与 partial 的 8 KiB 对齐。
	progressByteLimit = 8 << 10
	// progressSaveEveryBytes 是节流阈值：每累积这么多新字节才落盘一次。
	// 每个 delta 都 fsync 成本过高；1 KiB 兼顾崩溃粒度与写入开销。
	progressSaveEveryBytes = 1 << 10
)

// ProgressSink 持久化一个轮次内的进度检查点。实现归 app（它持有 session），
// agent 包只依赖这个中立接口。Save 失败被忽略——进度是纯优化，绝不影响运行。
type ProgressSink interface {
	Save(turn int, text string, toolCalls []string)
}

// ProgressSinkFunc 把普通函数适配为 ProgressSink。
type ProgressSinkFunc func(turn int, text string, toolCalls []string)

// Save 实现 ProgressSink。
func (f ProgressSinkFunc) Save(turn int, text string, toolCalls []string) {
	if f != nil {
		f(turn, text, toolCalls)
	}
}

// progressCapture 累积一次运行的流式文本，并在字节阈值处触发落盘。
// 与 partialCapture 一样用 mutex 保护：提供方的 onText 可能运行在独立 goroutine。
type progressCapture struct {
	mu        sync.Mutex
	sink      ProgressSink
	turn      int
	b         strings.Builder
	toolCalls []string
	lastSaved int // 上次落盘时的文本字节数
}

func newProgressCapture(sink ProgressSink, turn int) *progressCapture {
	return &progressCapture{sink: sink, turn: turn}
}

// Add 追加一个流式增量。文本按 rune 安全上限截断；当自上次落盘以来新增字节
// 达到阈值时，同步保存一次检查点。nil sink 是直接空操作。
func (c *progressCapture) Add(delta string) {
	if c == nil || c.sink == nil || delta == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := progressByteLimit - c.b.Len()
	if remaining <= 0 {
		return // 已达上限，后续增量不再累积（崩溃时保留的是最早 8 KiB）
	}
	if len(delta) > remaining {
		delta = delta[:runeSafePrefixLen(delta, remaining)]
	}
	_, _ = c.b.WriteString(delta)
	if c.b.Len()-c.lastSaved >= progressSaveEveryBytes {
		c.saveLocked()
	}
}

// SetTurn 更新当前轮次号。fallback 的每次模型请求都会经过 onDelta，
// 轮次在循环中推进，因此落盘前需同步为最新值。
func (c *progressCapture) SetTurn(turn int) {
	if c == nil || c.sink == nil {
		return
	}
	c.mu.Lock()
	c.turn = turn
	c.mu.Unlock()
}

// AddToolCall 记录一个工具调用描述（受数量上限约束），并在变更后落盘一次。
func (c *progressCapture) AddToolCall(description string) {
	if c == nil || c.sink == nil || description == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.toolCalls) >= maxProgressToolCallsLocal {
		return
	}
	c.toolCalls = append(c.toolCalls, description)
	c.saveLocked()
}

// maxProgressToolCallsLocal 与 session.maxProgressToolCalls 对齐（本包无法引用其私有常量）。
const maxProgressToolCallsLocal = 64

// saveLocked 在持锁状态下保存一个检查点副本。
func (c *progressCapture) saveLocked() {
	text := strings.Clone(c.b.String())
	var calls []string
	if len(c.toolCalls) > 0 {
		calls = append([]string(nil), c.toolCalls...)
	}
	c.lastSaved = c.b.Len()
	c.sink.Save(c.turn, text, calls)
}
