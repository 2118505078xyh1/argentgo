package agent

import (
	"fmt"
	"sync"
	"testing"
)

func TestSessionStatusRegistryZeroValue(t *testing.T) {
	// 零值注册表（内部 map 为 nil）即可读写，不应 panic。
	var r SessionStatusRegistry
	if got := r.Get("missing"); got != StatusIdle {
		t.Fatalf("Get(missing) = %q, want %q", got, StatusIdle)
	}
	if snap := r.Snapshot(); snap == nil || len(snap) != 0 {
		t.Fatalf("Snapshot() = %v, want non-nil empty map", snap)
	}

	r.Set("s", StatusWorking)
	if got := r.Get("s"); got != StatusWorking {
		t.Fatalf("Get(s) = %q, want %q", got, StatusWorking)
	}
}

func TestSessionStatusRegistryDeleteReturnsIdle(t *testing.T) {
	var r SessionStatusRegistry
	r.Set("s", StatusDone)
	r.Delete("s")
	if got := r.Get("s"); got != StatusIdle {
		t.Fatalf("Get(s) after Delete = %q, want %q", got, StatusIdle)
	}
	// 删除不存在的会话也应安全，且仍返回 idle。
	r.Delete("never-existed")
	if got := r.Get("never-existed"); got != StatusIdle {
		t.Fatalf("Get(unknown) = %q, want %q", got, StatusIdle)
	}
}

func TestSessionStatusRegistrySessionsIsolated(t *testing.T) {
	var r SessionStatusRegistry
	want := map[string]Status{
		"a": StatusIdle,
		"b": StatusPlanning,
		"c": StatusWorking,
		"d": StatusDone,
		"e": StatusError,
	}
	for id, status := range want {
		r.Set(id, status)
	}
	for id, status := range want {
		if got := r.Get(id); got != status {
			t.Fatalf("Get(%q) = %q, want %q", id, got, status)
		}
	}
	// 改写一个会话不应影响其它会话。
	r.Set("c", StatusError)
	if got := r.Get("b"); got != StatusPlanning {
		t.Fatalf("Get(b) = %q after updating c, want %q", got, StatusPlanning)
	}
}

func TestSessionStatusRegistrySnapshotIsCopy(t *testing.T) {
	var r SessionStatusRegistry
	r.Set("a", StatusWorking)
	r.Set("b", StatusDone)

	snap := r.Snapshot()
	if len(snap) != 2 || snap["a"] != StatusWorking || snap["b"] != StatusDone {
		t.Fatalf("Snapshot() = %v, want a=working b=done", snap)
	}

	// 调用方修改快照不得影响注册表内部状态。
	snap["a"] = StatusError
	delete(snap, "b")
	snap["new"] = StatusPlanning
	if got := r.Get("a"); got != StatusWorking {
		t.Fatalf("Get(a) = %q after mutating snapshot, want %q", got, StatusWorking)
	}
	if got := r.Get("b"); got != StatusDone {
		t.Fatalf("Get(b) = %q after mutating snapshot, want %q", got, StatusDone)
	}
	if got := r.Get("new"); got != StatusIdle {
		t.Fatalf("Get(new) = %q after mutating snapshot, want %q", got, StatusIdle)
	}

	// 两次快照彼此独立。
	second := r.Snapshot()
	second["a"] = StatusIdle
	if got := r.Get("a"); got != StatusWorking {
		t.Fatalf("Get(a) = %q after mutating second snapshot, want %q", got, StatusWorking)
	}
}

func TestSessionStatusRegistryConcurrentAccess(t *testing.T) {
	// 每个 goroutine 只拥有自己的会话 id，因此自身读回的值必须稳定；
	// 同时让 Snapshot 与写入并发，交由竞态检测器验证锁的正确性。
	var r SessionStatusRegistry
	const workers = 32
	const iterations = 200
	errCh := make(chan string, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			key := fmt.Sprintf("session-%d", id)
			for j := 0; j < iterations; j++ {
				r.Set(key, StatusWorking)
				if got := r.Get(key); got != StatusWorking {
					errCh <- fmt.Sprintf("Get(%q) = %q, want %q", key, got, StatusWorking)
					return
				}
				_ = r.Snapshot()

				r.Set(key, StatusDone)
				if got := r.Get(key); got != StatusDone {
					errCh <- fmt.Sprintf("Get(%q) = %q, want %q", key, got, StatusDone)
					return
				}
			}
			r.Delete(key)
			if got := r.Get(key); got != StatusIdle {
				errCh <- fmt.Sprintf("Get(%q) after Delete = %q, want %q", key, got, StatusIdle)
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}
}
