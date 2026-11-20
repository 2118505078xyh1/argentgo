package mcp

import "sync"

// inbox 弥合"消息可以到达"与"真正的处理器
// 已附加"之间的窗口：bind 之前对 buffer 的调用会被排队，并在 bind
// 安装处理器后按顺序重放；bind 之后的调用会被立即投递。
// 它是 omp 的 worker inbox（worker-host.ts）在 Go 中的对应物：在那里，
// 动态导入的 worker 模块在端口已经冲洗了同步发布的初始化握手之后，
// 才附加其消息处理器。
//
// 该类型有意独立于 stdioTransport 和线协议：
// 它是一个小巧的、线程安全的原语，可被任何有同样"早期消息"
// 竞态的生产者/消费者对复用。零值即可使用；
// 首次使用后不要复制 inbox。
type inbox[T any] struct {
	mu      sync.Mutex
	queued  []T
	ready   bool
	handler func(T)
}

// newInbox 创建一个没有附加处理器的空 inbox。
func newInbox[T any]() *inbox[T] { return &inbox[T]{} }

// buffer 记录 msg。bind 之前它只追加到队列；之后
// 直接调用附加的处理器。它从不在处理器上阻塞，因此
// 重入 buffer 的处理器不会死锁。
//
// 当 bind 重放队列时，处理器可能在与 buffer 不同的
// goroutine 上被调用，因此跨 goroutine 共享的处理器必须
// 安全并发使用（或依赖调用方串行化）。
func (in *inbox[T]) buffer(msg T) {
	in.mu.Lock()
	if !in.ready || in.handler == nil {
		in.queued = append(in.queued, msg)
		in.mu.Unlock()
		return
	}
	h := in.handler
	in.mu.Unlock()
	h(msg)
}

// bind 附加 handler 并按 FIFO 顺序重放目前为止缓冲的每条
// 消息。队列在同一临界区内、安装处理器时于锁下被摘下，因此每条缓冲消息
// 都恰好投递一次，由重放路径或直接路径之一完成，绝不会两者都投。
// 后续的 bind 会替换处理器，但不会重放更早的 bind 已投递的
// 消息。
func (in *inbox[T]) bind(handler func(T)) {
	in.mu.Lock()
	queued := in.queued
	in.queued = nil
	in.ready = true
	in.handler = handler
	in.mu.Unlock()

	for _, msg := range queued {
		handler(msg)
	}
}

// drain 移除并返回所有仍在等待处理器的消息，按 FIFO
// 顺序。它主要适用于轮询而非绑定的消费者；在此被 drain 的
// 消息不会被后续 bind 重放，因此任何消息都不会
// 被投递两次。
func (in *inbox[T]) drain() []T {
	in.mu.Lock()
	queued := in.queued
	in.queued = nil
	in.mu.Unlock()
	return queued
}
