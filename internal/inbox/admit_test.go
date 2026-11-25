package inbox

import (
	"path/filepath"
	"testing"
)

func TestAdmitFirstWins(t *testing.T) {
	box, err := New(Options{Path: filepath.Join(t.TempDir(), "inbox.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	item, admitted, err := box.Admit("s1", "req-1", "hello", DeliveryQueue)
	if err != nil || !admitted || item.Text != "hello" {
		t.Fatalf("first admit = %+v/%v/%v", item, admitted, err)
	}
	again, admitted, err := box.Admit("s1", "req-1", "different", DeliverySteer)
	if err != nil || admitted {
		t.Fatalf("duplicate must not be admitted: %+v/%v/%v", again, admitted, err)
	}
	if again.Text != "hello" || again.Delivery != DeliveryQueue {
		t.Fatalf("duplicate admission changed payload: %+v", again)
	}
	// 一次接纳只产生一对事件：enqueued + delivered。
	events := box.Events()
	if len(events) != 2 || events[0].Kind != KindEnqueued || events[1].Kind != KindDelivered {
		t.Fatalf("events = %+v", events)
	}
}

func TestAdmitReplaysUndeliveredAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inbox.jsonl")
	box, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃在 enqueued 与 delivered 之间：手工只写 enqueued 事件。
	box.mu.Lock()
	box.seq++
	ev := Event{Kind: KindEnqueued, SessionID: "s1", ItemID: "req-9", Text: "pending", Delivery: DeliverySteer, Sequence: box.seq}
	if err := box.append(ev); err != nil {
		box.mu.Unlock()
		t.Fatal(err)
	}
	box.apply(ev)
	box.mu.Unlock()

	restarted, err := New(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	item, admitted, err := restarted.Admit("s1", "req-9", "resent", DeliverySteer)
	if err != nil || admitted {
		t.Fatalf("undelivered replay = %+v/%v/%v", item, admitted, err)
	}
	if item.Text != "pending" {
		t.Fatalf("replay must use the original text, got %q", item.Text)
	}
	if len(restarted.Pending()) != 0 {
		t.Fatalf("replay must deliver the item: %+v", restarted.Pending())
	}
}

func TestAdmitRejectsEmptyID(t *testing.T) {
	box, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := box.Admit("s1", "  ", "text", DeliveryQueue); err != ErrInvalidItem {
		t.Fatalf("empty id error = %v", err)
	}
}
