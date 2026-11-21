package mcp

import (
	"sync"
	"time"
)

// toolCache 为单台服务器的工具发现列表提供带 TTL 的缓存。零值可直接使用，
// 但只有在 Spec.ToolCacheTTL > 0 时 ListTools 才会启用它；关闭时 ListTools
// 完全不触碰本类型，行为逐字节不变。
//
// 它实现了 single-flight：同一时刻只有一个调用真正访问传输，其他并发调用
// 等待其结果，从而把“缓存失效瞬间的惊群”收敛为一次 tools/list。缓存只在
// 发现成功后写入；失败不发布结果，等待者会重新竞争刷新权各自重试，保持
// 与无缓存时一致的错误语义。
type toolCache struct {
	mu        sync.Mutex
	now       func() time.Time // 可注入的时钟；nil 表示使用 time.Now
	tools     []Tool
	expiresAt time.Time
	valid     bool
	ready     chan struct{}
}

// get 返回缓存中的有效工具列表（副本）。
//
// 返回值 ok 为 true 表示命中缓存，调用方可直接使用。
// ok 为 false 表示当前调用者已成为刷新者：它必须执行一次发现，并在结束时
// 调用 set（成功）或 fail（失败）。若已有刷新在进行，get 会等待其完成后
// 重新判定，直到拿到有效缓存或成为新的刷新者。
func (t *toolCache) get() ([]Tool, bool) {
	t.mu.Lock()
	for {
		if t.valid && t.currentTime().Before(t.expiresAt) {
			out := append([]Tool(nil), t.tools...)
			t.mu.Unlock()
			return out, true
		}
		if t.ready == nil {
			// 没有有效缓存、也没有其他刷新：成为刷新者。
			t.ready = make(chan struct{})
			t.mu.Unlock()
			return nil, false
		}
		ready := t.ready
		t.mu.Unlock()
		<-ready
		t.mu.Lock()
	}
}

// set 发布一次成功发现的结果并唤醒所有等待者。
func (t *toolCache) set(tools []Tool, ttl time.Duration) {
	t.mu.Lock()
	t.tools = append([]Tool(nil), tools...)
	t.expiresAt = t.currentTime().Add(ttl)
	t.valid = true
	t.releaseWaitersLocked()
	t.mu.Unlock()
}

// fail 结束一次失败的发现并唤醒等待者，但不发布任何结果。等待者醒来后会
// 重新竞争刷新权并各自重试，与无缓存时的错误语义一致。
func (t *toolCache) fail() {
	t.mu.Lock()
	t.releaseWaitersLocked()
	t.mu.Unlock()
}

// releaseWaitersLocked 关闭并清空 ready 以唤醒等待者。调用方须持有 t.mu。
func (t *toolCache) releaseWaitersLocked() {
	if t.ready != nil {
		close(t.ready)
		t.ready = nil
	}
}

// currentTime 返回当前时间；测试可注入 now 以避免依赖真实时钟。
func (t *toolCache) currentTime() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}
