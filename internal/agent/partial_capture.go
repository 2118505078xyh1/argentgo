package agent

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// defaultPartialByteLimit 限制一次捕获保留多少流式文本。前缀是截断后 UI 仍能有意义展示的内容，
// 8 KiB 远超正常答案的长度，同时仍能为失控的流所占内存设定上限。
// 该上限在增量到达时即强制执行，而不只是在调用 Snapshot 时。
const defaultPartialByteLimit = 8 << 10

// partialCapture 累积单次模型尝试在失败或被取消前成功流式输出的文本。
// 它与一个模型和一个步骤绑定，因此切换回退模型会开启新的捕获，绝不会混用两次尝试的文本。
//
// 用互斥锁保护 builder：提供方的 onText 回调可能在循环执行 Snapshot 时运行于自己的
// goroutine 上，因此捕获必须在 -race 下安全，而无需循环与流做协调。
type partialCapture struct {
	mu        sync.Mutex
	model     string
	turn      int
	limit     int
	b         strings.Builder
	truncated bool
}

func newPartialCapture(model string, turn, limit int) *partialCapture {
	if limit <= 0 {
		limit = defaultPartialByteLimit
	}
	return &partialCapture{model: model, turn: turn, limit: limit}
}

// Add 追加一个流式增量，最多保留 limit 字节的完整 UTF-8 rune。
// 当某个增量会越过上限时，只保留最长的 rune 安全前缀，并锁存 truncated，
// 使消费者能把文本标记为不完整。
func (c *partialCapture) Add(delta string) {
	if delta == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	remaining := c.limit - c.b.Len()
	if remaining <= 0 {
		c.truncated = true
		return
	}
	if len(delta) <= remaining {
		_, _ = c.b.WriteString(delta)
		return
	}
	cut := runeSafePrefixLen(delta, remaining)
	_, _ = c.b.WriteString(delta[:cut])
	c.truncated = true
}

// partialSnapshot 是交给失败边界的值。它的 Text 是独立副本：
// 后续的 Add 无法修改调用方已读取的文本。
type partialSnapshot struct {
	Text       string
	StopReason string
	Model      string
	Turn       int
	Truncated  bool
}

// Snapshot 返回目前捕获的文本、其来源信息以及运行即将报告的停止原因。
// 停止原因由调用方提供，因为停止语义归循环所有；捕获只负责字节。
func (c *partialCapture) Snapshot(stopReason string) partialSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return partialSnapshot{
		Text:       strings.Clone(c.b.String()),
		StopReason: stopReason,
		Model:      c.model,
		Turn:       c.turn,
		Truncated:  c.truncated,
	}
}

// runeSafePrefixLen 返回满足 s[:n] 结束于 UTF-8 rune 边界的最大 n（<= maxBytes）。
// 从续字节回退可保证即使上限落在多字节序列中间，CJK 和 emoji 也保持完整。
func runeSafePrefixLen(s string, maxBytes int) int {
	if maxBytes >= len(s) {
		return len(s)
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// partialStopReason 把失败边界映射到循环将报告的停止原因：
// 被取消的上下文优先于携带它的提供方错误。
func partialStopReason(cancelled bool) string {
	if cancelled {
		return StopCancelled
	}
	return StopProviderError
}

// emitPartial 把捕获的前缀作为一个附加事件发布。没有流式输出时它是空操作，
// 因此未产生文本的失败保留原有的线上输出（仅错误 + 停止）。该事件不是终结事件：
// 循环仍会发出 EventStop 作为唯一的运行终结事件。
// 启用部分文本落盘后，同一快照还会通过注入的 Recorder 写入一条记录，使 resume 能看到这段文本；
// 无论开关如何，事件路径本身保持不变。
func (s *runState) emitPartial(snap partialSnapshot) {
	if snap.Text == "" {
		return
	}
	s.emit(Event{
		Kind:              EventPartial,
		Turn:              snap.Turn,
		Text:              snap.Text,
		Reason:            snap.StopReason,
		Partial:           true,
		PartialStopReason: snap.StopReason,
		PartialModel:      snap.Model,
		PartialTruncated:  snap.Truncated,
	})
	if partialRecordPersistenceEnabled() {
		s.record(partialRecordKind, partialRecordPayload(snap))
	}
}
