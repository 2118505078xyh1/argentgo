package app

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// DefaultDeltaBatchWindow 是给 TUI 的建议聚合窗口。它刻意比无界面/SSE 桥接窗口更短
// （opencode 使用 100ms）：全屏终端应在大约两帧 60Hz 时间内显示出第一个 token，
// 因此 32ms 既保持了批处理的价值，又不会让人感觉卡顿。
//
// 该值只是对调用方 time.Ticker 的建议，纯批处理逻辑本身并不使用它
// （它由驱动器显式调用 Flush）。
const DefaultDeltaBatchWindow = 32 * time.Millisecond

const (
	defaultBatchMaxDeltas = 64   // 每个合并事件的增量上限
	defaultBatchMaxRunes  = 4096 // 每个合并事件的 rune 上限（不是字节：CJK/emoji 安全）
)

// deltaBatcher 把一串 agent.EventTextDelta 合并为一个事件，其 Text 是这串增量的拼接。
// 它是纯聚合逻辑：不拥有 goroutine、通道或定时器，何时释放工作由调用方通过调用 Flush 驱动
// （例如来自 time.Ticker，见 runDeltaBatchTimer）。
//
// 契约：
//   - 只有 Text 非空的 EventTextDelta 参与。其他种类和空增量会被忽略（Add 报告 emit=false，状态不变）。
//   - Text 按 Add 的顺序拼接，因此合并事件的 Text 与逐个转发这些增量所得的结果逐字节一致。
//     永不会切断 rune：整个字符串被追加到 strings.Builder。
//   - Turn/Depth 继承自一串增量中的第一个。Turn 或 Depth 不同的增量会先刷新当前串，
//     使一串保持同质，且其边界在观察上与原始流一致。
//   - maxDeltas / maxRunes 是在增量之间测量的*软*刷新触发器；
//     单个大于 maxRunes 的增量会单独发出（从不被截断或拆分），这仍限制了一串能增长的长度。
//
// 该批处理器不适合并发使用。它的单 goroutine 驱动器（runDeltaBatchTimer）在同一个 goroutine 上
// 调用 Add 和 Flush；不要在没有外部同步的情况下从不同 goroutine 调用它们。
//
// 它也刻意没有接入 StartTUIEventBridge：见接线报告。保持未连接时，现有的桥接事件不变。
type deltaBatcher struct {
	maxDeltas int
	maxRunes  int

	buf         strings.Builder
	count       int // 当前串中的增量数
	runes       int // 当前串中的 rune 数
	turn        int
	depth       int
	havePending bool
}

// newDeltaBatcher 用给定的限制构建一个批处理器。非正的限制会回退到保守的默认值，
// 因此调用方不会意外创建出一个无界的聚合器。
func newDeltaBatcher(maxDeltas, maxRunes int) *deltaBatcher {
	if maxDeltas <= 0 {
		maxDeltas = defaultBatchMaxDeltas
	}
	if maxRunes <= 0 {
		maxRunes = defaultBatchMaxRunes
	}
	return &deltaBatcher{maxDeltas: maxDeltas, maxRunes: maxRunes}
}

// Add 把一个事件折叠进当前串。当加入 e 会超出某个限制（或跨越 Turn/Depth 边界）时，
// 它返回一个 emit=true 的合并事件：这会刷新已累积的串，并以 e 作为下一串的种子。
// 否则该事件被缓冲，并返回 emit=false。
func (b *deltaBatcher) Add(e agent.Event) (out agent.Event, emit bool) {
	if e.Kind != agent.EventTextDelta || e.Text == "" {
		return agent.Event{}, false
	}

	n := utf8.RuneCountInString(e.Text)
	if b.havePending {
		crossesBoundary := e.Turn != b.turn || e.Depth != b.depth
		hitsLimit := b.count >= b.maxDeltas || b.runes+n > b.maxRunes
		if crossesBoundary || hitsLimit {
			out, emit = b.Flush()
			b.start(e, n)
			return out, emit
		}
	}

	if !b.havePending {
		b.turn, b.depth = e.Turn, e.Depth
		b.havePending = true
	}
	b.buf.WriteString(e.Text)
	b.count++
	b.runes += n
	return agent.Event{}, false
}

// start 用 e 作为新串的种子。调用方在调用 start 之前，必须先刷新之前的串（如果有）。
func (b *deltaBatcher) start(e agent.Event, n int) {
	b.turn, b.depth = e.Turn, e.Depth
	b.buf.Reset()
	b.buf.WriteString(e.Text)
	b.count, b.runes, b.havePending = 1, n, true
}

// Flush 把待处理的串作为一个 EventTextDelta 释放。没有待处理内容时 ok 为 false，
// 此时返回的是零值事件。
func (b *deltaBatcher) Flush() (out agent.Event, ok bool) {
	if !b.havePending {
		return agent.Event{}, false
	}
	out = agent.Event{
		Kind:  agent.EventTextDelta,
		Turn:  b.turn,
		Depth: b.depth,
		Text:  b.buf.String(),
	}
	b.buf.Reset()
	b.count, b.runes, b.havePending = 0, 0, false
	return out, true
}

// Reset 丢弃待处理的串而不发出它。当运行被取消、尾部内容不应显示时很有用。
func (b *deltaBatcher) Reset() {
	b.buf.Reset()
	b.count, b.runes, b.havePending = 0, 0, false
}

// Pending 报告当前是否有串被缓冲。
func (b *deltaBatcher) Pending() bool { return b.havePending }

// Count 报告当前缓冲的增量数量（供测试/诊断使用）。
func (b *deltaBatcher) Count() int { return b.count }

// Runes 报告当前缓冲文本的 rune 长度。
func (b *deltaBatcher) Runes() int { return b.runes }

// runDeltaBatchTimer 是 deltaBatcher 的参考定时器驱动器。它在一个 goroutine 上拥有一个批处理器，
// 从 in 读取增量，通过 emit 发出合并事件，并在三种触发条件下刷新：配置的限制（通过 Add）、
// 每次窗口计时、以及关闭时（ctx 被取消或 in 关闭）处理尾部。
//
// 因为 Add 和 Flush 都在这一个 goroutine 上运行，所以无需加锁。
// 它刻意没有接入生产桥接路径；见接线报告。
func runDeltaBatchTimer(ctx context.Context, in <-chan agent.Event, window time.Duration, emit func(agent.Event)) {
	b := newDeltaBatcher(0, 0)
	if window <= 0 {
		window = DefaultDeltaBatchWindow
	}
	ticker := time.NewTicker(window)
	defer ticker.Stop()

	flush := func() {
		if out, ok := b.Flush(); ok {
			emit(out)
		}
	}
	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case e, open := <-in:
			if !open {
				flush()
				return
			}
			if out, ok := b.Add(e); ok {
				emit(out)
			}
		case <-ticker.C:
			flush()
		}
	}
}
