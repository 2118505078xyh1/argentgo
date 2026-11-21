package session

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestCheckoutAppendsToBranchWithoutCopying 验证 Checkout 到中间记录后，
// 新的追加挂到该分叉下，而不是复制历史或继续接在原来的叶节点之后。
func TestCheckoutAppendsToBranchWithoutCopying(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "checkout-branch")

	first, err := s.AppendRecord(KindUser, "first")
	if err != nil {
		t.Fatal(err)
	}
	middle, err := s.AppendRecord(KindAssistant, "middle")
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.AppendRecord(KindUser, "third")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := s.AppendRecord(KindAssistant, "leaf")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Checkout(middle.ID); err != nil {
		t.Fatalf("Checkout(%q): %v", middle.ID, err)
	}
	branch, err := s.AppendRecord(KindUser, "on branch")
	if err != nil {
		t.Fatal(err)
	}
	if branch.Parent != middle.ID {
		t.Fatalf("new record parent = %q, want %q", branch.Parent, middle.ID)
	}
	if branch.ID == first.ID || branch.ID == middle.ID || branch.ID == third.ID || branch.ID == leaf.ID {
		t.Fatalf("new record reused an existing id %q (history was copied?)", branch.ID)
	}

	// 记录数只增加一条：Checkout 不复制任何既有记录。
	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	records = withoutSchemaHeader(records)
	if len(records) != 6 { // header + first + middle + third + leaf + branch
		t.Fatalf("records = %d, want 6", len(records))
	}

	// 活动路径从根走到 middle 再到新分支，不包含原分支的 third/leaf。
	path, err := s.Path(branch.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantPathIDs := []string{first.Parent, first.ID, middle.ID, branch.ID}
	if len(path) != len(wantPathIDs) {
		t.Fatalf("branch path length = %d, want %d: %+v", len(path), len(wantPathIDs), path)
	}
	for i, want := range wantPathIDs {
		if path[i].ID != want {
			t.Fatalf("branch path[%d] = %q, want %q", i, path[i].ID, want)
		}
	}
	if active, ok := s.ActiveLeafID(); !ok || active != branch.ID {
		t.Fatalf("ActiveLeafID = %q, %v, want %q", active, ok, branch.ID)
	}

	// 原来的叶节点仍然可以从它自己的路径访问，历史没有被改动。
	original, err := s.Path(leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(original) != 5 { // header + first + middle + third + leaf
		t.Fatalf("original path length = %d, want 5", len(original))
	}
}

// TestCheckoutRejectsInvalidEntry 验证未知 id、空 id 与环都会被拒绝，
// 且失败时活动指针保持不变。
func TestCheckoutRejectsInvalidEntry(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "checkout-invalid")
	if err := s.Append(KindUser, "one"); err != nil {
		t.Fatal(err)
	}
	stable, ok := s.ActiveLeafID()
	if !ok {
		t.Fatal("session has no active leaf")
	}

	for _, id := range []string{"does-not-exist", ""} {
		if err := s.Checkout(id); err == nil {
			t.Fatalf("Checkout(%q) unexpectedly succeeded", id)
		}
		if active, _ := s.ActiveLeafID(); active != stable {
			t.Fatalf("failed Checkout(%q) changed active to %q, want %q", id, active, stable)
		}
	}

	// 构造一个 parent 成环的转录，Path 的环检查应当阻止 Checkout。
	dir := t.TempDir()
	path := filepath.Join(dir, "cycle.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "a", Parent: "b", Kind: KindUser, Data: json.RawMessage(`"a"`)},
		{ID: "b", Parent: "a", Kind: KindUser, Data: json.RawMessage(`"b"`)},
	})
	cyclic := &Session{dir: dir, id: "cycle", file: path}
	if err := cyclic.Checkout("a"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Checkout on cyclic entry = %v, want cycle error", err)
	}
}

// TestCheckoutConcurrentWithAppend 验证 Checkout 与并发 Append/读取可以安全共存：
// 不 panic、记录数准确、每条记录的 parent 都在文件内，且活动叶始终指向有效记录。
func TestCheckoutConcurrentWithAppend(t *testing.T) {
	s := newTestSession(t, t.TempDir(), "checkout-concurrent")

	const (
		baseCount   = 5
		writers     = 4
		perWriter   = 20
		checkouters = 3
		perCheckout = 30
	)
	var targets []string
	for i := 0; i < baseCount; i++ {
		rec, err := s.AppendRecord(KindUser, fmt.Sprintf("base-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, rec.ID)
	}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if _, err := s.AppendRecord(KindAssistant, "branch"); err != nil {
					t.Errorf("concurrent AppendRecord: %v", err)
					return
				}
			}
		}()
	}
	for c := 0; c < checkouters; c++ {
		wg.Add(1)
		go func(c int) {
			defer wg.Done()
			for i := 0; i < perCheckout; i++ {
				if err := s.Checkout(targets[(i+c)%len(targets)]); err != nil {
					t.Errorf("concurrent Checkout: %v", err)
					return
				}
				if leaf, ok := s.ActiveLeafID(); !ok || leaf == "" {
					t.Errorf("concurrent ActiveLeafID = %q, %v", leaf, ok)
					return
				}
				_ = s.Messages()
			}
		}(c)
	}
	wg.Wait()

	records, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	records = withoutSchemaHeader(records)
	want := 1 + baseCount + writers*perWriter // header + base + appended
	if len(records) != want {
		t.Fatalf("final records = %d, want %d", len(records), want)
	}
	ids := make(map[string]bool, len(records))
	for _, record := range records {
		if ids[record.ID] {
			t.Fatalf("duplicate record id %q", record.ID)
		}
		ids[record.ID] = true
	}
	for _, record := range records {
		if record.Parent != "" && !ids[record.Parent] {
			t.Errorf("record %q parent %q missing from file", record.ID, record.Parent)
		}
	}
	if leaf, ok := s.ActiveLeafID(); !ok || !ids[leaf] {
		t.Fatalf("final ActiveLeafID = %q, %v, want an existing record", leaf, ok)
	}
}

// TestOpenSessionAt 验证 Store.OpenSessionAt 打开会话后立即切换到指定记录，
// 后续追加挂到该记录之下。
func TestOpenSessionAt(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("at")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "one"); err != nil {
		t.Fatal(err)
	}
	middle, err := sess.AppendRecord(KindAssistant, "middle")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "last"); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenSessionAt(sess.ID(), middle.ID)
	if err != nil {
		t.Fatalf("OpenSessionAt: %v", err)
	}
	if leaf, ok := reopened.ActiveLeafID(); !ok || leaf != middle.ID {
		t.Fatalf("ActiveLeafID = %q, %v, want %q", leaf, ok, middle.ID)
	}
	added, err := reopened.AppendRecord(KindUser, "after checkout")
	if err != nil {
		t.Fatal(err)
	}
	if added.Parent != middle.ID {
		t.Fatalf("parent = %q, want %q", added.Parent, middle.ID)
	}
}

// TestOpenSessionAtInvalidEntry 验证非法 entry 会让 OpenSessionAt 直接报错，
// 并且不返回会话句柄。
func TestOpenSessionAtInvalidEntry(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("at")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindUser, "only"); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenSessionAt(sess.ID(), "missing")
	if err == nil {
		t.Fatal("OpenSessionAt with unknown entry unexpectedly succeeded")
	}
	if reopened != nil {
		t.Fatalf("OpenSessionAt returned a session on error: %+v", reopened)
	}
}
