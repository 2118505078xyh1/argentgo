package app

import (
	"strings"
	"sync"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/llm"
)

// 本文件实现 TODO #105「后台预压缩：提前写摘要、到阈值直接切换」。
// 学自 opencode：在距阈值尚有 lead 时，用后台 goroutine 提前算出压缩视图；
// 到阈值时若校验通过就直接交付缓存（零阻塞），否则回退到同步 MaybeCompact。
// 默认开启（ARGENT_BACKGROUND_COMPACT=0 时紧急关闭），默认即包装 compactor。

const backgroundCompactEnv = "ARGENT_BACKGROUND_COMPACT"

// backgroundCompactLead 是触发预计算的压力比例：达到阈值的 80% 即开始。
const backgroundCompactLead = 0.80

// backgroundCompactMaxTail 是缓存摘要时允许被折叠消息的最大条数；tail 过长
// （锚点之后又堆积大量消息）时拒绝切换，避免交付过期摘要。
const backgroundCompactMaxTail = 24

// backgroundCompactMaxFailures 是连续失败上限；达到后丢弃缓存并暂停预计算。
const backgroundCompactMaxFailures = 2

func backgroundCompactEnabled(env func(string) string) bool {
	if env == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env(backgroundCompactEnv))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// backgroundCompactor 包装 agent.Compactor：Observe 在接近阈值时异步准备摘要，
// MaybeCompact 在阈值处优先交付校验通过的缓存。
type backgroundCompactor struct {
	inner agent.Compactor
	lead  float64

	mu        sync.Mutex
	cached    []llm.Message // 预计算的压缩视图
	anchor    int           // 生成缓存时的消息条数（用于 tail 校验）
	preparing bool
	failures  int
}

// Observe 由 agent 的 CompactObserver 在每次工具批次后调用。
func (b *backgroundCompactor) Observe(messages []llm.Message, threshold int) {
	if b == nil || threshold <= 0 || b.inner == nil {
		return
	}
	if float64(agent.EstimateMessages(messages)) < b.lead*float64(threshold) {
		return
	}
	b.mu.Lock()
	if b.cached != nil || b.preparing || b.failures >= backgroundCompactMaxFailures {
		b.mu.Unlock()
		return
	}
	anchor := len(messages)
	b.preparing = true
	b.mu.Unlock()

	// 后台计算：inner.MaybeCompact 是纯本地文本提取，不调模型。
	result, changed := b.inner.MaybeCompact(append([]llm.Message(nil), messages...), agent.CompactWindow(threshold))

	b.mu.Lock()
	defer b.mu.Unlock()
	b.preparing = false
	if !changed || len(result) == 0 {
		b.failures++
		return
	}
	b.cached = result
	b.anchor = anchor
	b.failures = 0
}

// MaybeCompact 在阈值处优先交付缓存；tail 变化过大或缓存缺失时同步回退。
func (b *backgroundCompactor) MaybeCompact(messages []llm.Message, window int) ([]llm.Message, bool) {
	if b == nil || b.inner == nil {
		return messages, false
	}
	b.mu.Lock()
	cached := b.cached
	anchor := b.anchor
	b.cached = nil
	b.mu.Unlock()

	if cached != nil {
		tail := len(messages) - anchor
		if tail >= 0 && tail <= backgroundCompactMaxTail {
			return append([]llm.Message(nil), cached...), true
		}
		// tail 过长：丢弃过期缓存，回退同步路径。
	}
	return b.inner.MaybeCompact(messages, window)
}

// newBackgroundCompactor 默认包装 compactor，仅 =0 时不包装；返回观察者供 agent.Options 使用。
func newBackgroundCompactor(inner agent.Compactor, env func(string) string) (agent.Compactor, func([]llm.Message, int)) {
	if !backgroundCompactEnabled(env) || inner == nil {
		return inner, nil
	}
	b := &backgroundCompactor{inner: inner, lead: backgroundCompactLead}
	return b, b.Observe
}
