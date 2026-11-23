// Package inbox 提供**持久化的提示收件箱**（学自 opencode session/inbox.ts）。
//
// 背景：agent 循环已有内存 steering / FollowUp 通道（见 internal/agent/steer.go），
// 但进程崩溃会丢失「用户已发送、尚未投递」的提示。本包补上持久层：每次入队、
// 投递、取消都作为不可变事件追加到 JSONL，进程重启后从日志重建未投递队列。
//
// 投递语义：
//   - steer：可在安全步边界（工具批次之后、模型请求之前）抢占当前方向；
//   - queue：严格保序，仅在当前轮次结束、即将停止时才开启新轮次。
//
// 幂等：同 (sessionID, itemID) 只接受第一次 enqueued；重复入队原样返回已有项，
// 避免 SSE 重连/重试导致重复投递。
//
// 纯标准库、无 cgo、无后台 goroutine。文件 I/O 由调用方通过路径注入；
// fsync 在每次追加后调用（崩溃安全），可注入 false 加速测试。
package inbox

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Delivery 是投递模式。
type Delivery string

const (
	DeliverySteer Delivery = "steer"
	DeliveryQueue Delivery = "queue"
)

// Kind 是事件种类。
type Kind string

const (
	KindEnqueued  Kind = "enqueued"
	KindDelivered Kind = "delivered"
	KindCancelled Kind = "cancelled"
)

// Event 是一条持久事件（JSONL 一行）。
type Event struct {
	Kind      Kind      `json:"kind"`
	SessionID string    `json:"sessionId"`
	ItemID    string    `json:"itemId"`
	Text      string    `json:"text,omitempty"`
	Delivery  Delivery  `json:"delivery,omitempty"`
	Sequence  uint64    `json:"seq"`
	At        time.Time `json:"at"`
}

// Item 是待投递项（从事件流重建）。
type Item struct {
	ID       string
	Text     string
	Delivery Delivery
	Sequence uint64
	Enqueued time.Time
}

var (
	ErrInvalidItem = errors.New("inbox: invalid item")
	ErrNotFound    = errors.New("inbox: item not found")
	ErrCorruptLog  = errors.New("inbox: corrupt event log")
)

// Inbox 是一个会话的持久收件箱。并发安全。
type Inbox struct {
	mu      sync.Mutex
	path    string
	fsync   bool
	seq     uint64
	pending map[string]*Item
	order   []string // 按 Sequence 排序的 itemID
	events  []Event  // 完整事件流（供审计/回放）
}

// Options 配置。
type Options struct {
	// Path 是 JSONL 文件路径；为空表示纯内存模式（不持久化）。
	Path string
	// Fsync 控制每次追加后是否 fsync；默认 true。
	Fsync *bool
}

// New 创建收件箱。若 path 存在则从日志重建未投递项。
func New(opts Options) (*Inbox, error) {
	b := true
	if opts.Fsync != nil {
		b = *opts.Fsync
	}
	box := &Inbox{
		path:    opts.Path,
		fsync:   b,
		pending: make(map[string]*Item),
	}
	if opts.Path != "" {
		if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
			return nil, err
		}
		if err := box.replay(); err != nil {
			return nil, err
		}
	}
	return box, nil
}

// replay 从 JSONL 重建状态。损坏的行会跳过（尽力恢复），但完全不可读的文件报错。
func (b *Inbox) replay() error {
	f, err := os.Open(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // 跳过损坏行，尽力恢复
		}
		b.apply(ev)
	}
	return scanner.Err()
}

// apply 把事件应用到内存状态（调用方持锁）。
func (b *Inbox) apply(ev Event) {
	if ev.Sequence > b.seq {
		b.seq = ev.Sequence
	}
	switch ev.Kind {
	case KindEnqueued:
		if _, exists := b.pending[ev.ItemID]; exists {
			return // 幂等
		}
		b.pending[ev.ItemID] = &Item{
			ID: ev.ItemID, Text: ev.Text, Delivery: ev.Delivery,
			Sequence: ev.Sequence, Enqueued: ev.At,
		}
		b.order = append(b.order, ev.ItemID)
	case KindDelivered, KindCancelled:
		if _, exists := b.pending[ev.ItemID]; exists {
			delete(b.pending, ev.ItemID)
			b.removeOrder(ev.ItemID)
		}
	}
	b.events = append(b.events, ev)
}

func (b *Inbox) removeOrder(id string) {
	for i, v := range b.order {
		if v == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			return
		}
	}
}

// Enqueue 入队一条提示。id 为空时自动生成（基于 seq+时间）。
// 同 id 重复入队返回已有项（幂等），不产生新事件。
func (b *Inbox) Enqueue(sessionID, id, text string, delivery Delivery) (Item, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Item{}, fmt.Errorf("%w: empty text", ErrInvalidItem)
	}
	if delivery != DeliverySteer && delivery != DeliveryQueue {
		return Item{}, fmt.Errorf("%w: invalid delivery %q", ErrInvalidItem, delivery)
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if id == "" {
		id = fmt.Sprintf("inbox-%d-%d", time.Now().UnixNano(), b.seq+1)
	}
	if existing := b.pending[id]; existing != nil {
		return *existing, nil // 幂等
	}
	b.seq++
	ev := Event{
		Kind: KindEnqueued, SessionID: sessionID, ItemID: id,
		Text: text, Delivery: delivery, Sequence: b.seq, At: time.Now().UTC(),
	}
	if err := b.append(ev); err != nil {
		return Item{}, err
	}
	b.apply(ev)
	return *b.pending[id], nil
}

// append 把事件追加到 JSONL（持锁调用）。
func (b *Inbox) append(ev Event) error {
	if b.path == "" {
		return nil // 内存模式
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(b.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if b.fsync {
		_ = f.Sync()
	}
	return f.Close()
}

// Next 取出下一条应投递的提示：steer 优先（按 Sequence），其次 queue（按 Sequence）。
// 取出的项会写入 delivered 事件并从待投递集合移除。
func (b *Inbox) Next(sessionID string, allowSteer, allowQueue bool) (Item, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var chosen *Item
	// 先扫 steer（按序，第一个即可抢占），没有再扫 queue。
	if allowSteer {
		for _, id := range b.order {
			if item := b.pending[id]; item != nil && item.Delivery == DeliverySteer {
				chosen = item
				break
			}
		}
	}
	if chosen == nil && allowQueue {
		for _, id := range b.order {
			if item := b.pending[id]; item != nil && item.Delivery == DeliveryQueue {
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

// Cancel 取消一条未投递提示。
func (b *Inbox) Cancel(sessionID, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending[id] == nil {
		return ErrNotFound
	}
	b.seq++
	ev := Event{Kind: KindCancelled, SessionID: sessionID, ItemID: id, Sequence: b.seq, At: time.Now().UTC()}
	if err := b.append(ev); err != nil {
		return err
	}
	b.apply(ev)
	return nil
}

// Pending 返回当前待投递项（按 Sequence 排序的快照）。
func (b *Inbox) Pending() []Item {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Item, 0, len(b.order))
	for _, id := range b.order {
		if item := b.pending[id]; item != nil {
			out = append(out, *item)
		}
	}
	return out
}

// Events 返回完整事件流快照（供审计）。
func (b *Inbox) Events() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.events))
	copy(out, b.events)
	return out
}

// ChangeDelivery 修改未投递项的投递模式（如用户把 queue 改为 steer）。
func (b *Inbox) ChangeDelivery(id string, d Delivery) error {
	if d != DeliverySteer && d != DeliveryQueue {
		return fmt.Errorf("%w: invalid delivery %q", ErrInvalidItem, d)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	item := b.pending[id]
	if item == nil {
		return ErrNotFound
	}
	now := time.Now().UTC()
	// 用两个持久事件表达：取消旧项 + 以新模式重新入队（同 id）。
	b.seq++
	cancel := Event{Kind: KindCancelled, SessionID: "", ItemID: id, Sequence: b.seq, At: now}
	if err := b.append(cancel); err != nil {
		return err
	}
	b.apply(cancel)
	b.seq++
	enqueue := Event{Kind: KindEnqueued, SessionID: "", ItemID: id, Text: item.Text, Delivery: d, Sequence: b.seq, At: now}
	if err := b.append(enqueue); err != nil {
		return err
	}
	b.apply(enqueue)
	return nil
}
