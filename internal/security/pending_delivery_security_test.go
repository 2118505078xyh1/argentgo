package security

import (
	"fmt"
	"sync"
	"testing"
)

// digestForTest 返回测试用的参数摘要，与 pendingAt 落盘时使用的参数一致。
func digestForTest() string {
	return PendingArgsDigest(`{"command":"ls"}`)
}

// TestResolveWithDigestMatchDelivers 校验参数摘要一致时：返回 true、命中记录被
// 移除、决定被记录、等待者被唤醒。
func TestResolveWithDigestMatchDelivers(t *testing.T) {
	store := newPendingTestStore(t)
	const session, id = "sess-1", "call_1"
	if err := store.Add(pendingAt(id, "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ch, cancel := store.RegisterWaiter(session, id)
	defer cancel()

	if !store.ResolveWithDigest(session, id, true, digestForTest()) {
		t.Fatal("ResolveWithDigest with matching digest returned false")
	}
	select {
	case got := <-ch:
		if !got {
			t.Fatal("waiter got deny, want allow")
		}
	default:
		t.Fatal("ResolveWithDigest with matching digest did not wake the waiter")
	}
	got, err := store.List(session)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("matching-digest Resolve left %d pending records, want 0", len(got))
	}
}

// TestResolveWithDigestMismatchDoesNotDeliver 校验纵深绑定：摘要不符时返回 false，
// 不投递等待者，也不改动磁盘记录。
func TestResolveWithDigestMismatchDoesNotDeliver(t *testing.T) {
	store := newPendingTestStore(t)
	const session, id = "sess-1", "call_1"
	if err := store.Add(pendingAt(id, "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ch, cancel := store.RegisterWaiter(session, id)
	defer cancel()

	wrong := PendingArgsDigest(`{"command":"rm -rf /"}`)
	if store.ResolveWithDigest(session, id, true, wrong) {
		t.Fatal("ResolveWithDigest with mismatched digest returned true")
	}
	select {
	case got := <-ch:
		t.Fatalf("mismatched digest delivered answer %v to waiter", got)
	default:
	}
	if got := len(store.waiters); got != 1 {
		t.Fatalf("waiters after mismatched-digest Resolve = %d, want 1", got)
	}
	items, err := store.List(session)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("mismatched-digest Resolve changed the store: %+v", items)
	}
}

// TestResolveWithDigestUnknownIDDoesNotDeliver 校验未知 id（即使带着某个摘要）
// 既不投递也不改动存储。
func TestResolveWithDigestUnknownIDDoesNotDeliver(t *testing.T) {
	store := newPendingTestStore(t)
	if err := store.Add(pendingAt("call_real", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ch, cancel := store.RegisterWaiter("sess-1", "call_forged")
	defer cancel()

	if store.ResolveWithDigest("sess-1", "call_forged", true, digestForTest()) {
		t.Fatal("ResolveWithDigest with unknown id returned true")
	}
	select {
	case got := <-ch:
		t.Fatalf("unknown id delivered answer %v to waiter", got)
	default:
	}
}

// TestResolveSecurityCJKSession 校验 CJK 会话：命中时正常投递、伪造 id 时不投递。
// session 经过哈希命名文件、等待者键使用原始字符串，两条路径都必须处理 UTF-8。
func TestResolveSecurityCJKSession(t *testing.T) {
	store := newPendingTestStore(t)
	const session, id = "会话-甲", "审批_001"
	p := PendingApproval{
		ID:         id,
		Session:    session,
		Tool:       "bash",
		ArgsDigest: digestForTest(),
		Created:    "2026-01-01T00:00:01Z",
	}
	if err := store.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// 伪造 id 不得投递。
	forgedCh, cancelForged := store.RegisterWaiter(session, "审批_forged")
	defer cancelForged()
	if err := store.Resolve(session, "审批_forged", true); err != nil {
		t.Fatalf("Resolve forged id: %v", err)
	}
	select {
	case got := <-forgedCh:
		t.Fatalf("forged id delivered answer %v to waiter", got)
	default:
	}

	// 命中 id 必须投递。
	realCh, cancelReal := store.RegisterWaiter(session, id)
	defer cancelReal()
	if err := store.Resolve(session, id, false); err != nil {
		t.Fatalf("Resolve real id: %v", err)
	}
	select {
	case got := <-realCh:
		if got {
			t.Fatal("waiter got allow, want deny")
		}
	default:
		t.Fatal("Resolve of real id in CJK session did not wake the waiter")
	}
}

// TestResolveWithDigestConcurrent 让多组 (session, id) 并发解析：摘要匹配的应投递，
// 摘要不符的不投递。共享互斥量下每条记录按各自 id 独立判定，不串扰。
func TestResolveWithDigestConcurrent(t *testing.T) {
	const n = 32
	store := newPendingTestStore(t)
	type outcome struct {
		ch      <-chan bool
		cancel  func()
		wantOK  bool
		granted bool
	}
	outcomes := make([]outcome, n)
	var wg sync.WaitGroup
	errCh := make(chan error, n)

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("call_%02d", i)
		session := fmt.Sprintf("会话-%d", i%4)
		granted := i%2 == 0
		// 偶数命中正确摘要，奇数使用错误摘要。
		digest := digestForTest()
		wantOK := i%2 == 0
		if !wantOK {
			digest = PendingArgsDigest(`{"command":"wrong"}`)
		}
		p := PendingApproval{
			ID:         id,
			Session:    session,
			Tool:       "bash",
			ArgsDigest: digestForTest(),
			Created:    fmt.Sprintf("2026-01-01T00:00:%02dZ", i),
		}
		if err := store.Add(p); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
		ch, cancel := store.RegisterWaiter(session, id)
		outcomes[i] = outcome{ch: ch, cancel: cancel, wantOK: wantOK, granted: granted}

		wg.Add(1)
		go func(i int, session, id, digest string, granted, wantOK bool) {
			defer wg.Done()
			gotOK := store.ResolveWithDigest(session, id, granted, digest)
			if gotOK != wantOK {
				errCh <- fmt.Errorf("ResolveWithDigest(%s) returned %v, want %v", id, gotOK, wantOK)
			}
		}(i, session, id, digest, granted, wantOK)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	for i, o := range outcomes {
		o.cancel()
		select {
		case got := <-o.ch:
			if !o.wantOK {
				t.Errorf("waiter %d received answer %v despite mismatched digest", i, got)
			} else if got != o.granted {
				t.Errorf("waiter %d got %v, want %v", i, got, o.granted)
			}
		default:
			if o.wantOK {
				t.Errorf("waiter %d was not woken on matching digest", i)
			}
		}
	}
}
