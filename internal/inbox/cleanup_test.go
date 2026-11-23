package inbox

import (
	"testing"
	"time"
)

func TestCleanupMaxPending(t *testing.T) {
	b, _ := New(Options{})
	for i := 0; i < 10; i++ {
		b.Enqueue("s", "", "text", DeliveryQueue)
	}
	removed, err := b.Cleanup(CleanupPolicy{MaxPending: 3})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 7 {
		t.Fatalf("removed = %d, want 7", removed)
	}
	if got := len(b.Pending()); got != 3 {
		t.Fatalf("pending = %d, want 3", got)
	}
	// 淘汰的项通过 cancelled 事件可审计。
	cancelled := 0
	for _, ev := range b.Events() {
		if ev.Kind == KindCancelled {
			cancelled++
		}
	}
	if cancelled != 7 {
		t.Fatalf("cancelled events = %d, want 7", cancelled)
	}
}

func TestCleanupTTL(t *testing.T) {
	b, _ := New(Options{})
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	policy := CleanupPolicy{TTL: time.Hour, Clock: func() time.Time { return now }}

	// 用 EnqueueWithCleanup 注入固定时钟。
	b.EnqueueWithCleanup("s", "old", "old text", DeliveryQueue, policy)
	now = now.Add(2 * time.Hour)
	b.EnqueueWithCleanup("s", "new", "new text", DeliveryQueue, policy)

	pending := b.Pending()
	if len(pending) != 1 || pending[0].ID != "new" {
		t.Fatalf("after TTL cleanup = %+v, want only new", pending)
	}
}

func TestCleanupKeepsNewestOnOverflow(t *testing.T) {
	b, _ := New(Options{})
	for i := 0; i < 5; i++ {
		b.Enqueue("s", "id-"+string(rune('a'+i)), "text", DeliveryQueue)
	}
	b.Cleanup(CleanupPolicy{MaxPending: 2})
	pending := b.Pending()
	if len(pending) != 2 || pending[0].ID != "id-d" || pending[1].ID != "id-e" {
		t.Fatalf("kept = %+v, want newest two", pending)
	}
}

func TestEnqueueWithCleanup(t *testing.T) {
	b, _ := New(Options{})
	for i := 0; i < 5; i++ {
		b.Enqueue("s", "", "text", DeliveryQueue)
	}
	// 入队前清理到 2 条，再入队 1 条 → 3 条。
	item, err := b.EnqueueWithCleanup("s", "", "fresh", DeliveryQueue, CleanupPolicy{MaxPending: 2})
	if err != nil {
		t.Fatal(err)
	}
	if item.Text != "fresh" {
		t.Fatalf("item = %+v", item)
	}
	if got := len(b.Pending()); got != 3 {
		t.Fatalf("pending = %d, want 3", got)
	}
}

func TestCleanupDefaults(t *testing.T) {
	b, _ := New(Options{})
	for i := 0; i < 5; i++ {
		b.Enqueue("s", "", "text", DeliveryQueue)
	}
	// 零值策略使用默认上限（256），不应淘汰。
	removed, err := b.Cleanup(CleanupPolicy{})
	if err != nil || removed != 0 {
		t.Fatalf("default cleanup removed %d: %v", removed, err)
	}
}

func TestStats(t *testing.T) {
	b, _ := New(Options{})
	b.Enqueue("s", "a", "text", DeliveryQueue)
	s := b.Stats()
	if s.Pending != 1 || s.Events != 1 || s.Sequence != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
