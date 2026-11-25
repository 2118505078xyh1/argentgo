package inbox

import (
	"strings"
	"time"
)

// Admit 幂等接纳并立即投递一条提示。它把"入队 + 投递"合成一次操作，
// 供执行入口在真正运行之前声明一次持久 admission（清单 #193）。
//
// first-admission-wins：同一 (sessionID, id) 只产生一次 enqueued/delivered 事件。
// 重复调用返回 admitted=false，调用方据此跳过重复执行。若上次接纳后尚未投递
// （例如崩溃在两步之间），重复调用只原样投递一次，不会再次接纳。
//
// id 必填——空 id 意味着放弃幂等语义，因此直接拒绝而不是自动生成。
func (b *Inbox) Admit(sessionID, id, text string, delivery Delivery) (Item, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Item{}, false, ErrInvalidItem
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Item{}, false, ErrInvalidItem
	}
	if delivery != DeliverySteer && delivery != DeliveryQueue {
		return Item{}, false, ErrInvalidItem
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	var first Item
	for _, ev := range b.events {
		if ev.SessionID != sessionID || ev.ItemID != id {
			continue
		}
		if ev.Kind == KindEnqueued && first.ID == "" {
			first = Item{ID: id, Text: ev.Text, Delivery: ev.Delivery, Sequence: ev.Sequence, Enqueued: ev.At}
		}
		if ev.Kind == KindEnqueued || ev.Kind == KindDelivered {
			// 已接纳过：若仍在待投递集合则原样投递，否则返回第一次的载荷。
			if item := b.pending[id]; item != nil {
				chosen, err := b.deliverFirstLocked(sessionID)
				return chosen, false, err
			}
			return first, false, nil
		}
	}

	b.seq++
	ev := Event{
		Kind: KindEnqueued, SessionID: sessionID, ItemID: id,
		Text: text, Delivery: delivery, Sequence: b.seq, At: time.Now().UTC(),
	}
	if err := b.append(ev); err != nil {
		return Item{}, false, err
	}
	b.apply(ev)
	chosen, err := b.deliverFirstLocked(sessionID)
	if err != nil {
		return Item{}, false, err
	}
	return chosen, true, nil
}

// deliverFirstLocked 在已持锁时投递一条待投递项：steer 优先、其次 queue。
func (b *Inbox) deliverFirstLocked(sessionID string) (Item, error) {
	var chosen *Item
	for _, id := range b.order {
		if item := b.pending[id]; item != nil && item.Delivery == DeliverySteer {
			chosen = item
			break
		}
	}
	if chosen == nil {
		for _, id := range b.order {
			if item := b.pending[id]; item != nil {
				chosen = item
				break
			}
		}
	}
	if chosen == nil {
		return Item{}, ErrNotFound
	}
	b.seq++
	ev := Event{Kind: KindDelivered, SessionID: sessionID, ItemID: chosen.ID, Sequence: b.seq, At: time.Now().UTC()}
	if err := b.append(ev); err != nil {
		return Item{}, err
	}
	b.apply(ev)
	return *chosen, nil
}
