package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

const (
	// eventHistoryCap 限制回放日志的大小。在游标之后，最多回放
	// 这么多个最近事件，然后才开始实时流。
	eventHistoryCap = 256
	// eventSubCap 是每个订阅者的普通缓冲配额。它 >=
	// eventHistoryCap，使回放中的订阅者在 hub 持有写锁时也不会阻塞，
	// 并在第一个实时事件到达之前留有
	// 余量。
	eventSubCap = 512
	// eventTerminalSlots 是在 eventSubCap 之外为必达的
	// 终态事件（EventStop）预留的容量。订阅者普通配额满后会被淘汰，
	// 因此预留槽位让最终的终态事件能够送达慢速订阅者，
	// 而不是把它淘汰、让无界面的客户端永远
	// 等待完成事件。
	eventTerminalSlots = 1
	// eventHeartbeatInterval 让空闲的 SSE 连接能穿过代理保持存活。
	eventHeartbeatInterval = 15 * time.Second
)

// EventHub 是一个保序、事件溯源的广播器，带有界回放日志。
//
// Publish 为每个事件分配单调递增的序列号，并按相同顺序
// 投递给每个订阅者。Subscribe 先回放游标之后的历史
// 尾部，随后跟随实时事件。订阅者通道是带缓冲的；普通缓冲配额满的
// 订阅者会被淘汰（关闭其通道并移除），而不是拖慢
// 发布者或其他订阅者。终态事件（EventStop）是
// 例外：它们必达，使用预留槽位，而不是淘汰
// 已满的订阅者。
// a full subscriber.
//
// hub 可安全地被多个发布者和订阅者并发使用。
// 它有意与传输层无关：HTTP/SSE 和 --rpc 前端
// 都可以通过 Config.Events 共享同一个实例。
//
// 除 Subscribe/SubscribeAfter 外，SubscribeTyped 允许订阅者只接收指定 Kind 的事件，
// 用于节省带宽与解码开销；它不改变其它订阅者的行为。
type EventHub struct {
	mu      sync.RWMutex
	seq     uint64
	subs    map[chan agent.Event]*subscriber
	history []agent.Event
	evicted uint64
	// eventLog 为 opt-in 挂载的磁盘事件日志。nil 时发布与 SSE 回放逐字节不变。
	eventLog *EventLog
}

// AttachEventLog 给 hub 挂载磁盘事件日志，使 SSE 客户端能用 Last-Event-ID
// 在超出内存 256 条窗口后继续重放。未调用时 hub 完全保持在内存模式。
func (h *EventHub) AttachEventLog(log *EventLog) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.eventLog = log
	if log != nil {
		log.initEntries()
	}
}

// subscriber 描述一个订阅者的投递偏好。types 为 nil 或空时不过滤，接收全部事件；
// 否则只接收 Kind 命中该集合的事件。所有订阅者共用同一个发布循环，因此保序。
type subscriber struct {
	types map[string]struct{}
}

// matches 报告该订阅者是否希望接收 e。过滤只依据事件的 Kind 字符串，
// 因此 CJK 等非 ASCII 类型名与内置 EventKind 一视同仁。
func (s *subscriber) matches(e agent.Event) bool {
	if len(s.types) == 0 {
		return true
	}
	_, ok := s.types[string(e.Kind)]
	return ok
}

// NewEventHub 返回一个可供使用的空 hub。
func NewEventHub() *EventHub {
	return &EventHub{
		subs:    make(map[chan agent.Event]*subscriber),
		history: make([]agent.Event, 0, eventHistoryCap),
	}
}

// Seq 返回最近发布的事件的序列号。
// 在第一次 Publish 之前为零。
func (h *EventHub) Seq() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.seq
}

// Evicted 返回到目前为止被淘汰的慢速订阅者数量。
// 它主要用于可观测性和测试。
func (h *EventHub) Evicted() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.evicted
}

// Publish 把 e 追加到有界回放日志，分配下一个
// 序列号，并广播给所有当前订阅者。它返回分配的
// 序列号。
//
// 投递是非阻塞的。普通缓冲配额已满的订阅者
// 会被淘汰（关闭其通道）并计数，必达的终态事件除外，
// 它们仍会送达。这绝不会延迟其他订阅者。
func (h *EventHub) Publish(e agent.Event) uint64 {
	h.mu.Lock()
	var log *EventLog
	if h.eventLog != nil {
		log = h.eventLog
	}

	h.seq++
	if len(h.history) == eventHistoryCap {
		// 滑动窗口：丢弃最旧的事件以保持日志有界。
		copy(h.history, h.history[1:])
		h.history[len(h.history)-1] = e
	} else {
		h.history = append(h.history, e)
	}

	for ch, sub := range h.subs {
		// 类型过滤：不关心的事件直接跳过，既不占用订阅者缓冲，也不触发淘汰。
		// 终态事件的“必达”语义只在订阅者确实订阅了该类型时才有意义。
		if !sub.matches(e) {
			continue
		}
		if e.IsTerminal() {
			// 必达：绝不因终态事件而淘汰订阅者，否则
			// 未排空进度事件、无界面的客户端会
			// 错过完成信号。只要订阅者仍只持有普通配额，预留槽位就是空闲的；
			// 若连它也被占满，则丢弃最旧的已缓冲进度事件来腾出空间
			// （仍可从回放日志恢复），而不是丢失
			// 终态事件。
			// 终态事件。
			//
			// 两个操作都必须保持非阻塞。订阅者的消费者与 Publish
			// 并发地排空 ch，因此在观察到通道已满
			// 与移除最旧事件之间，消费者可能已将其完全排空。
			// 此时阻塞的 `<-ch` 会在持有 h.mu 时挂起，
			// 永久拖慢其他发布者和订阅者。发布者由 h.mu 串行化，
			// 消费者只接收，因此释放至多一个槽位后，
			// 末尾的发送总有空间——但仍保持非阻塞，
			// 以使该保证
			// 无条件成立。
			select {
			case ch <- e:
			default:
				select {
				case <-ch: // 丢弃最旧的缓冲事件以释放预留槽位
				default: // 消费者同时已排空；无需丢弃
				}
				select {
				case ch <- e:
				default: // 持有 h.mu 时不可达；绝不拖慢 hub
				}
			}
			continue
		}
		if len(ch) >= eventSubCap {
			// 慢速消费者：关闭它并继续。Go 中 range 时删除是
			// 安全的，且 stop() 中的 map 成员检查可防止订阅者
			// 同时退订时发生重复关闭。
			close(ch)
			delete(h.subs, ch)
			h.evicted++
			continue
		}
		ch <- e // cap > eventSubCap，因此低于配额的订阅者不会阻塞。
	}
	seq := h.seq
	h.mu.Unlock()

	// 磁盘日志写入在释放 hub 锁之后进行，避免慢磁盘阻塞发布与订阅者。
	// 未挂载日志时 log 为 nil，整段跳过，默认发布路径逐字节不变。
	if log != nil {
		_ = log.append(seq, e)
	}
	return seq
}

// Subscribe 登记一个新订阅者。序列号大于 after 的历史事件
// 会先被回放（最近的尾部，受 eventHistoryCap 限制），
// 随后实时事件按发布顺序到达。
//
// 返回的 stop 函数是幂等的：它只退订并关闭通道一次，
// 即使订阅者已被淘汰，且可与 Publish 或自身
// 并发调用。订阅者被淘汰时，其通道会在缓冲事件排空后
// 干净地关闭。
func (h *EventHub) Subscribe(after uint64) (<-chan agent.Event, func()) {
	ch, stop, _ := h.SubscribeAfter(after)
	return ch, stop
}

// seqEvent 携带事件及其分配的序列号，仅供 SSE 的 id: 帧使用。
type seqEvent struct {
	seq uint64
	ev  agent.Event
}

// memoryBase 返回当前内存回放窗口中最旧事件的序列号（无事件时为 seq+1）。
// 客户端游标早于该值时，窗口外的事件需要从磁盘日志回放。
func (h *EventHub) memoryBase() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.history) == 0 {
		return h.seq + 1
	}
	return h.seq - uint64(len(h.history)) + 1
}

// SubscribeAfterSeq 与 SubscribeAfter 语义相同，但额外在每条事件上附带其
// 真实序列号，供 SSE 写出 id: 帧。调用方应先确认 after 不早于 memoryBase()，
// 这样回放从 after+1 连续编号才与真实 seq 一致。
func (h *EventHub) SubscribeAfterSeq(after uint64) (<-chan seqEvent, func(), uint64) {
	raw, stop, seqAt := h.SubscribeAfter(after)
	out := make(chan seqEvent, cap(raw))
	done := make(chan struct{})
	var once sync.Once
	wrappedStop := func() {
		once.Do(func() {
			stop()
			close(done)
		})
	}
	go func() {
		defer close(out)
		cursor := after
		for ev := range raw {
			cursor++
			select {
			case out <- seqEvent{seq: cursor, ev: ev}:
			case <-done:
				return
			}
		}
	}()
	return out, wrappedStop, seqAt
}

// registerSubscriber 在调用方已持有 h.mu 的前提下登记一个订阅者。
func (h *EventHub) registerSubscriber(ch chan agent.Event, sub *subscriber) {
	if h.subs == nil {
		h.subs = make(map[chan agent.Event]*subscriber)
	}
	h.subs[ch] = sub
}

// unsubscribe 幂等地退订并关闭 ch；即使订阅者已被淘汰也不会重复关闭。
func (h *EventHub) unsubscribe(ch chan agent.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		close(ch)
		delete(h.subs, ch)
	}
}

// newStop 返回一个幂等、可与 Publish 或其它调用并发执行的退订函数。
func (h *EventHub) newStop(ch chan agent.Event) func() {
	var once sync.Once
	return func() {
		once.Do(func() { h.unsubscribe(ch) })
	}
}

// SubscribeAfter 登记一个订阅者，并返回注册那一刻的 hub 序列号。
// 该值是返回流的安全续传游标：到它为止、且比 after 更新的每个事件
// 都已复制进 ch，并将由此订阅者投递，而且没有更新
// 的事件被写入客户端。
// 事件被写入客户端。
//
// 调用方必须用这个返回的 seq 做"你在这里"握手（SSE 的
// server.connected 帧、RPC 的 event/subscribe 结果），而不是在订阅后
// 读取 Seq()。发布可能落在 Subscribe 与单独的 Seq() 调用之间；
// 那会给客户端一个指向仍在 ch 中排队事件*之后*的游标。
// 从该游标重连的客户端会完全跳过它们。
// 在同一把锁下同时捕获两者，使握手无缝隙（最坏情况下
// 重连只是重放一个已见过的事件，绝不会丢失事件）。
func (h *EventHub) SubscribeAfter(after uint64) (<-chan agent.Event, func(), uint64) {
	ch := make(chan agent.Event, eventSubCap+eventTerminalSlots)

	h.mu.Lock()
	h.registerSubscriber(ch, &subscriber{}) // 无类型过滤：接收全部事件
	seqAt := h.seq

	// 保留的最旧事件的序列号是 h.seq - len(history) + 1。
	// 计算该基数，只回放比游标更新的事件。注册
	// 与回放共享同一把锁，因此不会有实时事件夹在两者之间
	// 破坏顺序。回放数量 <= eventHistoryCap < eventSubCap，所以
	// 这些发送不会阻塞。
	base := h.seq - uint64(len(h.history)) + 1
	start := 0
	if after >= base {
		// 游标处或之前的已保留事件数量。在转换为 int *之前*先在
		// uint64 域中钳制：after 由客户端提供，
		// 可能高达 MaxUint64，而 int(after-base+1) 会回绕为
		// 负数，使回放循环用负偏移索引 history，
		// 导致调用方 panic（在 --rpc 上是整个进程）。
		skip := after - base + 1
		if skip >= uint64(len(h.history)) {
			start = len(h.history)
		} else {
			start = int(skip)
		}
	}
	for i := start; i < len(h.history); i++ {
		ch <- h.history[i]
	}
	h.mu.Unlock()

	return ch, h.newStop(ch), seqAt
}

// SubscribeTyped 登记一个只接收指定类型事件的订阅者。types 中每一项都与
// agent.Event.Kind 的字符串形式精确比较（区分大小写，支持 CJK 等任意 Unicode
// 类型名）；types 为空或 nil 时不过滤，行为与 Subscribe 一致——接收全部事件。
//
// 该订阅只投递订阅时刻之后发布的实时事件，不回放历史；需要游标续传的调用方
// 应使用 SubscribeAfter。投递与所有订阅者共用同一个发布循环，因此保序。
// 返回的 stop 函数幂等，可与 Publish 或自身并发调用；被淘汰的订阅者其通道会在
// 缓冲事件排空后干净地关闭。
func (h *EventHub) SubscribeTyped(types []string) (<-chan agent.Event, func()) {
	ch := make(chan agent.Event, eventSubCap+eventTerminalSlots)
	sub := &subscriber{}
	if len(types) > 0 {
		sub.types = make(map[string]struct{}, len(types))
		for _, typ := range types {
			sub.types[typ] = struct{}{}
		}
	}

	h.mu.Lock()
	h.registerSubscriber(ch, sub)
	h.mu.Unlock()

	return ch, h.newStop(ch)
}

// NewEventHandler 把 hub 暴露为 Server-Sent Events 流。
//
// 第一帧始终是 `event: server.connected`，其 data 中带 `{"seq":N}`，
// 使客户端知道当前游标并可用 ?after=N 重连。
// N 与订阅以原子方式一起捕获（见 SubscribeAfter）：它是
// 已经为此客户端排队最后一个事件，绝不是在订阅与写帧
// 之间发布的事件。之后的每一帧都是
// `event: <kind>`，data 为 agent.Event 的 JSON。
// 空闲流每 15 秒发出一条 `: heartbeat` 注释，请求 context 的
// 取消会在客户端断开时退订它。
func NewEventHandler(hub *EventHub) http.Handler {
	return newEventHandlerInterval(hub, eventHeartbeatInterval)
}

// newEventHandlerInterval 是心跳间隔的测试接缝。
func newEventHandlerInterval(hub *EventHub, interval time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		var after uint64
		// Last-Event-ID 是 SSE 标准的续传头；?after 查询参数保留兼容。
		// 头优先，未带或非法时回退到查询参数。
		if raw := r.Header.Get("Last-Event-ID"); raw != "" {
			if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
				after = n
			}
		}
		if after == 0 {
			if raw := r.URL.Query().Get("after"); raw != "" {
				if n, err := strconv.ParseUint(raw, 10, 64); err == nil {
					after = n
				}
			}
		}

		// 磁盘日志为 opt-in：未挂载时走完全原样的内存回放路径，逐字节不变。
		hub.mu.RLock()
		log := hub.eventLog
		hub.mu.RUnlock()

		if log == nil {
			// 默认路径：不带 id: 帧，与历史行为逐字节一致。
			// 注意：SubscribeAfter 必须先于任何 w.Header()/WriteHeader 调用——
			// 某些 ResponseWriter 在 Header() 首次被调用时发布事件（握手竞态测试），
			// 提前触碰 header 会让游标多算一条事件。
			ch, stop, seqAt := hub.SubscribeAfter(after)
			defer stop()
			header := w.Header()
			header.Set("Content-Type", "text/event-stream")
			header.Set("Cache-Control", "no-cache")
			header.Set("Connection", "keep-alive")
			header.Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			if _, err := fmt.Fprintf(w, "event: server.connected\ndata: {\"seq\":%d}\n\n", seqAt); err != nil {
				return
			}
			flusher.Flush()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
						return
					}
					flusher.Flush()
				case ev, ok := <-ch:
					if !ok {
						return
					}
					data, err := json.Marshal(ev)
					if err != nil {
						continue
					}
					if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, data); err != nil {
						return
					}
					flusher.Flush()
				}
			}
		}

		// opt-in 日志路径：先确定游标再设置 header（同上的握手竞态语义）。
		header := w.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		header.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		// 先补放内存中已淘汰、但磁盘仍留存的事件，带真实 id:。
		base := hub.memoryBase()
		replayFrom := after
		var lastReplayed uint64 = after
		if base > 0 && replayFrom+1 < base {
			for _, entry := range log.after(replayFrom) {
				if entry.Seq >= base {
					break // 其余由内存回放覆盖，避免重复
				}
				data, err := json.Marshal(entry.Event)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", entry.Seq, entry.Event.Kind, data); err != nil {
					return
				}
				lastReplayed = entry.Seq
			}
			flusher.Flush()
		}

		ch, stop, seqAt := hub.SubscribeAfterSeq(lastReplayed)
		defer stop()
		if _, err := fmt.Fprintf(w, "event: server.connected\ndata: {\"seq\":%d}\n\n", seqAt); err != nil {
			return
		}
		flusher.Flush()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case se, ok := <-ch:
				if !ok {
					return
				}
				data, err := json.Marshal(se.ev)
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", se.seq, se.ev.Kind, data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}

// WireAgentEvents 在后台 goroutine 中把 agent 事件通道桥接到 hub。
// 当 ctx 被取消或 emit 关闭时停止。返回的
// 通道在 goroutine 退出时关闭，调用方可以等待桥接
// 停止；该返回值可以忽略。
//
// 给 internal/app（唯一 import 所有包的包）的接线说明：
//
//	hub := server.NewEventHub()
//	server.WireAgentEvents(hub, agentEvents, ctx)
//	cfg.Events = hub // 由 --serve (/v1/event) 和 --rpc (event/subscribe) 共享
func WireAgentEvents(hub *EventHub, emit <-chan agent.Event, ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 兜底恢复：Publish 或事件消费中的 panic 不得终结整个进程。
		// close(done) 先注册、后执行（后进先出），因此即便 panic 也一定
		// 关闭 done，等待桥接停止的调用方不会挂起。此处只需记录日志：
		// hub 的订阅生命周期由调用方的 stop() 负责，与桥接无关。
		defer func() {
			if r := recover(); r != nil {
				logRecoveredPanic("wire agent events", r)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-emit:
				if !ok {
					return
				}
				hub.Publish(ev)
			}
		}
	}()
	return done
}
