package pluginapi

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// TestTxRegistryCommitIsAtomic 验证整批提交：全部成功时生效且 generation 递增，
// 只要有一项与已有条目重名就整批失败，目标注册表与 generation 都不变。
func TestTxRegistryCommitIsAtomic(t *testing.T) {
	target := &Registry[int]{}
	if err := target.Register("existing", 0, func() int { return 1 }); err != nil {
		t.Fatalf("seed: %v", err)
	}
	reg := NewTxRegistry(target)

	bad := reg.Begin()
	if err := bad.Register("new", 1, func() int { return 2 }); err != nil {
		t.Fatalf("stage new: %v", err)
	}
	if err := bad.Register("existing", 2, func() int { return 3 }); err != nil {
		t.Fatalf("stage conflicting: %v", err)
	}
	if err := bad.Commit(); err == nil {
		t.Fatal("commit with conflict succeeded")
	}
	if got := target.Names(); len(got) != 1 || got[0] != "existing" {
		t.Fatalf("names after failed commit = %q", got)
	}
	if got := reg.Generation(); got != 0 {
		t.Fatalf("generation after failed commit = %d, want 0", got)
	}

	good := reg.Begin()
	if err := good.Register("a", 1, func() int { return 10 }); err != nil {
		t.Fatalf("stage a: %v", err)
	}
	if err := good.Register("b", 2, func() int { return 20 }); err != nil {
		t.Fatalf("stage b: %v", err)
	}
	if err := good.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := target.Names(); len(got) != 3 {
		t.Fatalf("names after commit = %q", got)
	}
	if got := reg.Generation(); got != 1 {
		t.Fatalf("generation = %d, want 1", got)
	}
}

// TestTxRegistryRejectsDuplicateWithinTransaction 确认事务内部重名在暂存阶段
// 就被拒绝，不会留到提交时才失败。
func TestTxRegistryRejectsDuplicateWithinTransaction(t *testing.T) {
	reg := NewTxRegistry(&Registry[string]{})
	tx := reg.Begin()
	if err := tx.Register("dup", 0, func() string { return "a" }); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := tx.Register("dup", 1, func() string { return "b" })
	if err == nil || !strings.Contains(err.Error(), "in transaction") {
		t.Fatalf("duplicate err = %v", err)
	}
}

// TestTxRegistryDiscard 确认 discard 后待提交项不生效，事务也不能再提交。
func TestTxRegistryDiscard(t *testing.T) {
	target := &Registry[int]{}
	reg := NewTxRegistry(target)
	tx := reg.Begin()
	if err := tx.Register("discarded", 0, func() int { return 1 }); err != nil {
		t.Fatalf("stage: %v", err)
	}
	tx.Discard()
	if err := tx.Commit(); err == nil {
		t.Fatal("commit after discard succeeded")
	}
	if target.Len() != 0 {
		t.Fatalf("target len = %d, want 0", target.Len())
	}
}

// TestHandleInvalidatesOnCommit 验证句柄失效语义：旧句柄在新提交后返回
// ErrStaleHandle，新句柄仍可读取注册表。
func TestHandleInvalidatesOnCommit(t *testing.T) {
	reg := NewTxRegistry(&Registry[int]{})
	old := reg.Handle()
	if _, err := old.Factories(); err != nil {
		t.Fatalf("fresh handle: %v", err)
	}

	tx := reg.Begin()
	if err := tx.Register("m", 0, func() int { return 1 }); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if old.Valid() {
		t.Fatal("old handle remained valid after commit")
	}
	if _, err := old.Factories(); !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("old handle err = %v, want ErrStaleHandle", err)
	}
	current := reg.Handle()
	if !current.Valid() {
		t.Fatal("new handle should be valid")
	}
	factories, err := current.Factories()
	if err != nil || len(factories) != 1 {
		t.Fatalf("new handle factories = %d, err = %v", len(factories), err)
	}
}

// TestTxRegistryConcurrentCommits 验证并发事务下只有一个提交能成功写入同名
// 条目，且 generation 恰好递增一次。
func TestTxRegistryConcurrentCommits(t *testing.T) {
	reg := NewTxRegistry(&Registry[int]{})
	var wg sync.WaitGroup
	var successes, failures int
	var mu sync.Mutex

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx := reg.Begin()
			if err := tx.Register("only", 0, func() int { return 1 }); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
				return
			}
			err := tx.Commit()
			mu.Lock()
			if err == nil {
				successes++
			} else {
				failures++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successful commits = %d, want 1", successes)
	}
	if got := reg.Generation(); got != 1 {
		t.Fatalf("generation = %d, want 1", got)
	}
}
