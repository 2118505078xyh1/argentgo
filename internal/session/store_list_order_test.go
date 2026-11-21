package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeOrderedSession 写入一份所有记录共享 stamp 的转录，使
// metaFromRecords 为这里每个会话派生相同的记录内 Updated。
// mtime 被显式设置，以模拟磁盘上真实的写入顺序——这正是两个会话在同一秒创建时，
// --resume 所需要的。
func writeOrderedSession(t *testing.T, dir, id, title string, stamp, mtime time.Time, withFollowUp bool) string {
	t.Helper()
	path := filepath.Join(dir, id+fileExt)
	records := []Record{
		{ID: "h1", TS: stamp, Kind: KindHeader, Data: json.RawMessage(`{"title":"` + title + `"}`)},
	}
	if withFollowUp {
		records = append(records, Record{
			ID: "h2", Parent: "h1", TS: stamp, Kind: KindUser,
			Data: json.RawMessage(`{"role":"user","content":"newer"}`),
		})
	}
	writeRawSession(t, path, records)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
	return path
}

func TestListOrdersEqualUpdatedByModTime(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	older := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)

	// first 的 id 排在 second 之后，因此仅按 id 决胜会把它
	// 排在最前，尽管 second 是较晚写入的。
	first := "20261120-100000-ffff"
	second := "20261120-100000-0000"
	writeOrderedSession(t, dir, first, "first", stamp, older, false)
	writeOrderedSession(t, dir, second, "second", stamp, newer, true)

	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("List returned %d metas, want 2", len(metas))
	}
	if !metas[0].Updated.Equal(metas[1].Updated) {
		t.Fatalf("test setup: Updated differ: %v vs %v", metas[0].Updated, metas[1].Updated)
	}
	if metas[0].ID != second || metas[1].ID != first {
		t.Fatalf("order = [%s %s], want [%s %s] (newest by mtime first)",
			metas[0].ID, metas[1].ID, second, first)
	}
}

func TestListTiesByIDWhenTimestampsMatch(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	same := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	low := "20261120-100000-0000"
	high := "20261120-100000-ffff"
	writeOrderedSession(t, dir, low, "low", stamp, same, false)
	writeOrderedSession(t, dir, high, "high", stamp, same, true)

	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("List returned %d metas, want 2", len(metas))
	}
	if !metas[0].Modified.Equal(metas[1].Modified) {
		t.Fatalf("test setup: ModTime differ: %v vs %v", metas[0].Modified, metas[1].Modified)
	}
	if metas[0].ID != high || metas[1].ID != low {
		t.Fatalf("order = [%s %s], want [%s %s] (id descending when fully tied)",
			metas[0].ID, metas[1].ID, high, low)
	}
}

func TestListFirstEntryIsResumeTarget(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	older := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)

	first := "20261120-100000-ffff"
	second := "20261120-100000-0000"
	writeOrderedSession(t, dir, first, "first", stamp, older, false)
	writeOrderedSession(t, dir, second, "second", stamp, newer, true)

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	metas, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("List returned no sessions")
	}

	// 这镜像 openOrCreateSession 的 --resume 路径：取 List()[0] 并
	// 重新打开它。它必须是 mtime 较晚的那个会话。
	sess, err := store.OpenSession(metas[0].ID)
	if err != nil {
		t.Fatalf("OpenSession(%q): %v", metas[0].ID, err)
	}
	if sess.ID() != second {
		t.Fatalf("resumed %q, want %q", sess.ID(), second)
	}
	records, err := sess.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	found := false
	for _, record := range records {
		if record.Kind == KindUser {
			found = true
		}
	}
	if !found {
		t.Fatalf("resumed session %q lacks the later appended record", sess.ID())
	}
}

// TestNextRecordTSMonotonic 验证记录时间戳在连续调用下严格递增。
// 系统时钟在同一 tick 内会返回完全相同的值；钳制后每次调用都必须晚于上一次，
// 这是 List 仅凭 Updated 就能稳定排序、无需依赖有歧义的次级决胜的前提。
func TestNextRecordTSMonotonic(t *testing.T) {
	prev := nextRecordTS()
	for i := 0; i < 1000; i++ {
		cur := nextRecordTS()
		if !cur.After(prev) {
			t.Fatalf("nextRecordTS[%d] = %v, want strictly after %v", i, cur, prev)
		}
		prev = cur
	}
}

// TestListSameSecondOrderStable 复现第三类假失败：多个会话在同一秒（甚至同一时钟 tick）
// 内创建并追加。单调时间戳保证最近追加的 derived 始终排在首位，且列表严格按 Updated 降序。
// 修复前约 2.7% 的迭代会因三条记录的 TS 与 mtime 全部相等而退化到随机 id 比较，从而失败。
func TestListSameSecondOrderStable(t *testing.T) {
	const iterations = 100
	for iter := 0; iter < iterations; iter++ {
		dir := t.TempDir()
		alpha := newTestSession(t, dir, "alpha task")
		beta := newTestSession(t, dir, "beta task")
		derived := newTestSession(t, dir, "")
		if err := derived.Append(KindUser, strings.Repeat("x", 120)); err != nil {
			t.Fatalf("iter %d Append: %v", iter, err)
		}
		metas, err := List(dir)
		if err != nil {
			t.Fatalf("iter %d List: %v", iter, err)
		}
		if len(metas) != 3 {
			t.Fatalf("iter %d listed %d sessions, want 3", iter, len(metas))
		}
		if metas[0].ID != derived.ID() {
			t.Fatalf("iter %d first listed = %q, want most recently appended %q (alpha=%s beta=%s)",
				iter, metas[0].ID, derived.ID(), alpha.ID(), beta.ID())
		}
		for i := 1; i < len(metas); i++ {
			if !metas[i-1].Updated.After(metas[i].Updated) {
				t.Fatalf("iter %d list not strictly newest-first by Updated: %+v", iter, metas)
			}
		}
	}
}
