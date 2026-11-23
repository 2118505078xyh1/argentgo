package inbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnqueueAndNextQueue(t *testing.T) {
	b, _ := New(Options{})
	if _, err := b.Enqueue("s1", "a", "first", DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Enqueue("s1", "b", "second", DeliveryQueue); err != nil {
		t.Fatal(err)
	}
	got, err := b.Next("s1", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "a" || got.Text != "first" {
		t.Fatalf("first = %+v", got)
	}
	got, _ = b.Next("s1", true, true)
	if got.ID != "b" {
		t.Fatalf("second = %+v", got)
	}
	if _, err := b.Next("s1", true, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty next err = %v", err)
	}
}

func TestSteerTakesPriority(t *testing.T) {
	b, _ := New(Options{})
	b.Enqueue("s1", "q", "queued", DeliveryQueue)
	b.Enqueue("s1", "s", "steering", DeliverySteer)
	got, err := b.Next("s1", true, true)
	if err != nil || got.ID != "s" {
		t.Fatalf("steer should win: %+v %v", got, err)
	}
}

func TestNextDeliveryFlags(t *testing.T) {
	b, _ := New(Options{})
	b.Enqueue("s1", "q", "queued", DeliveryQueue)
	if _, err := b.Next("s1", true, false); !errors.Is(err, ErrNotFound) {
		t.Fatal("queue must not deliver when not allowed")
	}
	b.Enqueue("s1", "s", "steer", DeliverySteer)
	got, _ := b.Next("s1", false, true)
	if got.ID != "q" {
		t.Fatalf("with steer disabled, queue should deliver: %+v", got)
	}
}

func TestEnqueueIdempotent(t *testing.T) {
	b, _ := New(Options{})
	first, _ := b.Enqueue("s1", "same-id", "text", DeliveryQueue)
	second, err := b.Enqueue("s1", "same-id", "different", DeliverySteer)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Text != first.Text || second.Delivery != first.Delivery {
		t.Fatalf("duplicate enqueue changed item: %+v vs %+v", first, second)
	}
	if len(b.Pending()) != 1 {
		t.Fatalf("pending = %d, want 1", len(b.Pending()))
	}
	if len(b.Events()) != 1 {
		t.Fatalf("events = %d, want 1 (no duplicate event)", len(b.Events()))
	}
}

func TestCancel(t *testing.T) {
	b, _ := New(Options{})
	b.Enqueue("s1", "x", "cancel me", DeliveryQueue)
	if err := b.Cancel("s1", "x"); err != nil {
		t.Fatal(err)
	}
	if len(b.Pending()) != 0 {
		t.Fatal("canceled item still pending")
	}
	if err := b.Cancel("s1", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double cancel err = %v", err)
	}
}

func TestChangeDelivery(t *testing.T) {
	b, _ := New(Options{})
	b.Enqueue("s1", "x", "text", DeliveryQueue)
	if err := b.ChangeDelivery("x", DeliverySteer); err != nil {
		t.Fatal(err)
	}
	items := b.Pending()
	if len(items) != 1 || items[0].Delivery != DeliverySteer || items[0].Text != "text" {
		t.Fatalf("after change = %+v", items)
	}
}

func TestPersistenceAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.jsonl")
	noFsync := false
	b, err := New(Options{Path: path, Fsync: &noFsync})
	if err != nil {
		t.Fatal(err)
	}
	b.Enqueue("s1", "a", "persist-a", DeliveryQueue)
	b.Enqueue("s1", "b", "persist-b", DeliverySteer)
	b.Next("s1", true, true) // 投递 a

	// 重建
	b2, err := New(Options{Path: path, Fsync: &noFsync})
	if err != nil {
		t.Fatal(err)
	}
	pending := b2.Pending()
	if len(pending) != 1 || pending[0].ID != "a" {
		t.Fatalf("recovered pending = %+v, want only a (b was delivered by steer priority)", pending)
	}
	got, err := b2.Next("s1", true, true)
	if err != nil || got.Text != "persist-a" {
		t.Fatalf("recovered deliver = %+v %v", got, err)
	}
}

func TestCorruptLineRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.jsonl")
	os.WriteFile(path, []byte("not-json\n{\"kind\":\"enqueued\",\"itemId\":\"ok\",\"text\":\" surv\",\"delivery\":\"queue\",\"seq\":1}\n"), 0o600)
	noFsync := false
	b, err := New(Options{Path: path, Fsync: &noFsync})
	if err != nil {
		t.Fatal(err)
	}
	items := b.Pending()
	if len(items) != 1 || items[0].ID != "ok" {
		t.Fatalf("corrupt line recovery = %+v", items)
	}
}

func TestInvalidInputs(t *testing.T) {
	b, _ := New(Options{})
	if _, err := b.Enqueue("s", "x", "  ", DeliveryQueue); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("empty text err = %v", err)
	}
	if _, err := b.Enqueue("s", "x", "text", "bogus"); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("bad delivery err = %v", err)
	}
	if err := b.ChangeDelivery("missing", DeliverySteer); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing change err = %v", err)
	}
}

func TestConcurrentEnqueue(t *testing.T) {
	b, _ := New(Options{})
	done := make(chan error, 50)
	for i := 0; i < 50; i++ {
		go func(n int) {
			_, err := b.Enqueue("s", "", "text", DeliveryQueue)
			done <- err
		}(i)
	}
	for i := 0; i < 50; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(b.Pending()) != 50 {
		t.Fatalf("pending = %d, want 50", len(b.Pending()))
	}
}
