package security

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPendingStoreResolveWakesRegisteredWaiter(t *testing.T) {
	for _, granted := range []bool{true, false} {
		name := "deny"
		if granted {
			name = "allow"
		}
		t.Run(name, func(t *testing.T) {
			store := newPendingTestStore(t)
			const session, id = "sess-1", "call_1"
			if err := store.Add(pendingAt(id, "2026-01-01T00:00:01Z")); err != nil {
				t.Fatalf("Add: %v", err)
			}
			ch, cancel := store.RegisterWaiter(session, id)
			defer cancel()

			if err := store.Resolve(session, id, granted); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			select {
			case got := <-ch:
				if got != granted {
					t.Fatalf("waiter got %v, want %v", got, granted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Resolve did not wake the registered waiter")
			}

			// 重放的决定不得投递第二次回答：等待者已被第一次 Resolve 消耗，
			// 这使 Resolve 具有幂等性。
			if err := store.Resolve(session, id, !granted); err != nil {
				t.Fatalf("replayed Resolve: %v", err)
			}
			select {
			case got := <-ch:
				t.Fatalf("waiter received a duplicate answer %v", got)
			default:
			}
		})
	}
}

func TestResolveUnknownIDDoesNotDeliver(t *testing.T) {
	// 安全回归（R50 P0）：id 可预测，伪造的未知 id 不得翻转已注册等待者的答案。
	// 投递发生在 defer 中、发生在 Resolve 返回之前，因此 Resolve 一返回就能用
	// 非阻塞 select 判定是否发生了投递。
	store := newPendingTestStore(t)
	// 会话中确实存在待处理请求，但伪造 id 与它们都不相同。
	if err := store.Add(pendingAt("call_real", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	const forgedID = "call_forged"
	ch, cancel := store.RegisterWaiter("sess-1", forgedID)
	defer cancel()
	if err := store.Resolve("sess-1", forgedID, true); err != nil {
		t.Fatalf("Resolve unknown id: %v", err)
	}
	select {
	case got := <-ch:
		t.Fatalf("unknown id delivered answer %v to waiter", got)
	default:
	}
	// 未知 id 既不消耗等待者，也不改动磁盘记录。
	if got := len(store.waiters); got != 1 {
		t.Fatalf("waiters after unknown-id Resolve = %d, want 1", got)
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "call_real" {
		t.Fatalf("unknown-id Resolve changed the store: %+v", got)
	}
}

func TestPendingStoreResolveWithoutWaiterDoesNotBlock(t *testing.T) {
	store := newPendingTestStore(t)
	if err := store.Add(pendingAt("call_1", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- store.Resolve("sess-1", "call_1", true)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve blocked when no waiter was registered")
	}
}

func TestPendingStoreCancelledWaiterIsNotDelivered(t *testing.T) {
	store := newPendingTestStore(t)
	const session, id = "sess-1", "call_1"
	ch, cancel := store.RegisterWaiter(session, id)
	cancel()
	cancel() // 幂等：defer 加上显式调用是安全的
	if err := store.Add(pendingAt(id, "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Resolve(session, id, true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case got := <-ch:
		t.Fatalf("cancelled waiter received %v", got)
	default:
	}
}

func TestPendingStoreReregisterReplacesWaiter(t *testing.T) {
	store := newPendingTestStore(t)
	const session, id = "sess-1", "call_1"
	// 命中记录是投递的前提；先持久化再注册，模拟真实的 Ask 流程。
	if err := store.Add(pendingAt(id, "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	first, cancelFirst := store.RegisterWaiter(session, id)
	second, cancelSecond := store.RegisterWaiter(session, id)
	defer cancelFirst()
	defer cancelSecond()
	// 取消被替换的等待者不得移除当前的等待者。
	cancelFirst()
	if err := store.Resolve(session, id, false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case got := <-second:
		if got {
			t.Fatal("replacement waiter got allow, want deny")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement waiter was not woken")
	}
	select {
	case got := <-first:
		t.Fatalf("replaced waiter received %v", got)
	default:
	}
}

func TestPendingStoreConcurrentWaitersEachGetTheirAnswer(t *testing.T) {
	const n = 32
	const session = "sess-1"
	store := newPendingTestStore(t)
	channels := make([]<-chan bool, n)
	cancels := make([]func(), n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("call_%d", i)
		if err := store.Add(pendingAt(id, fmt.Sprintf("2026-01-01T00:00:%02dZ", i))); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
		ch, cancel := store.RegisterWaiter(session, id)
		channels[i], cancels[i] = ch, cancel
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()

	errCh := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Resolve(session, fmt.Sprintf("call_%d", i), i%2 == 0); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Resolve: %v", err)
	}
	for i, ch := range channels {
		want := i%2 == 0
		select {
		case got := <-ch:
			if got != want {
				t.Errorf("waiter %d got %v, want %v", i, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d was not woken", i)
		}
	}
}

func TestPendingStoreWaitersCleanedUpAfterResolveAndCancel(t *testing.T) {
	store := newPendingTestStore(t)
	cancelA, cancelB := func() {}, func() {}
	_, cancelA = store.RegisterWaiter("s", "a")
	_, cancelB = store.RegisterWaiter("s", "b")
	cancelA()
	if got := len(store.waiters); got != 1 {
		t.Fatalf("waiters after cancel = %d, want 1", got)
	}
	// b 必须存在于待处理列表中，命中才会投递并消耗其等待者。
	if err := store.Add(PendingApproval{ID: "b", Session: "s", Created: "2026-01-01T00:00:01Z"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Resolve("s", "b", true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := len(store.waiters); got != 0 {
		t.Fatalf("waiters after Resolve = %d, want 0", got)
	}
	cancelB() // 等待者被消耗后为空操作
	if got := len(store.waiters); got != 0 {
		t.Fatalf("waiters after redundant cancel = %d, want 0", got)
	}
}
