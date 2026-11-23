package inbox

import (
	"errors"
	"time"
)

// 本文件为持久收件箱补上**容量与 TTL 治理**（学自 opencode inbox 的
// "只存未消费工作" 约束）：防止用户长时间不消费导致 JSONL 无限增长，
// 以及已入队很久的提示在投递时早已过时。
//
// 治理不改变投递语义，只在 Enqueue 时触发：先淘汰过期项，再在超过上限时
// 淘汰最旧的项。每次淘汰都会被记录为 cancelled 事件（可审计、可恢复）。

const (
	// DefaultMaxPending 是默认的最大未投递项数。
	DefaultMaxPending = 256
	// DefaultTTL 是默认的入队有效期；超过 TTL 的项在下次 Enqueue 时淘汰。
	DefaultTTL = 24 * time.Hour
)

// CleanupPolicy 控制容量与 TTL。零值字段回退到默认值。
type CleanupPolicy struct {
	MaxPending int
	TTL        time.Duration
	Clock      func() time.Time
}

// cleanup 应用治理策略，返回淘汰数量（持锁调用，已写入事件）。
func (b *Inbox) cleanup(policy CleanupPolicy) (int, error) {
	maxPending := policy.MaxPending
	if maxPending <= 0 {
		maxPending = DefaultMaxPending
	}
	ttl := policy.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	now := time.Now
	if policy.Clock != nil {
		now = policy.Clock
	}
	removed := 0

	// 1) TTL 过期淘汰。
	for _, id := range append([]string(nil), b.order...) {
		item := b.pending[id]
		if item == nil {
			continue
		}
		// 用真实经过时间判断 TTL（Enqueued 由 Enqueue 用 time.Now 写入），
		// policy.Clock 只用于事件的时间戳，不参与 TTL 比较——避免注入时钟与
		// 入队时间基准不一致导致误判。
		if time.Since(item.Enqueued) > ttl {
			b.seq++
			ev := Event{Kind: KindCancelled, ItemID: id, Sequence: b.seq, At: now().UTC()}
			if err := b.append(ev); err != nil {
				return removed, err
			}
			b.apply(ev)
			removed++
		}
	}

	// 2) 容量超限淘汰最旧项。
	for len(b.order) > maxPending {
		oldest := b.order[0]
		b.seq++
		ev := Event{Kind: KindCancelled, ItemID: oldest, Sequence: b.seq, At: now().UTC()}
		if err := b.append(ev); err != nil {
			return removed, err
		}
		b.apply(ev)
		removed++
	}
	return removed, nil
}

// EnqueueWithCleanup 在入队前应用治理策略。
func (b *Inbox) EnqueueWithCleanup(sessionID, id, text string, delivery Delivery, policy CleanupPolicy) (Item, error) {
	b.mu.Lock()
	if _, err := b.cleanup(policy); err != nil {
		b.mu.Unlock()
		return Item{}, err
	}
	b.mu.Unlock()
	return b.Enqueue(sessionID, id, text, delivery)
}

// Cleanup 手动触发治理（供后台维护或 CLI 调用）。
func (b *Inbox) Cleanup(policy CleanupPolicy) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cleanup(policy)
}

// Stats 返回治理相关的统计。
type Stats struct {
	Pending  int
	Events   int
	Sequence uint64
}

func (b *Inbox) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Stats{Pending: len(b.pending), Events: len(b.events), Sequence: b.seq}
}

// ErrPolicyInvalid 表示治理策略非法。
var ErrPolicyInvalid = errors.New("inbox: invalid cleanup policy")
