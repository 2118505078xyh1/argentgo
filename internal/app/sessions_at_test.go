package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
)

// newAtTestStore 在临时目录里建一个存储与会话，转录为
// header -> first(user) -> middle(assistant) -> last(user)。
// 返回 store、会话以及 middle 的记录 id（可用作 --at 的分叉点）。
func newAtTestStore(t *testing.T) (*session.Store, *session.Session, string) {
	t.Helper()
	// 这些用例验证 --at 恢复的活动叶；分支摘要的额外记录会偏移活动叶，属另一特性，隔离之。
	t.Setenv("ARGENT_BRANCH_SUMMARY", "0")
	store, err := session.Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("at-resume")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "first"); err != nil {
		t.Fatal(err)
	}
	middle, err := sess.AppendRecord(session.KindAssistant, "middle")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "last"); err != nil {
		t.Fatal(err)
	}
	return store, sess, middle.ID
}

// TestOpenOrCreateSessionAtCheckout 验证 --session X --at Y 走 OpenSessionAt：
// 恢复句柄的活动叶节点停在 Y，后续追加挂在 Y 之下形成新分支，而不复制历史。
func TestOpenOrCreateSessionAtCheckout(t *testing.T) {
	store, sess, middleID := newAtTestStore(t)

	got, err := openOrCreateSession(store, cli.Options{Session: sess.ID(), At: middleID}, "ignored")
	if err != nil {
		t.Fatalf("openOrCreateSession with --at: %v", err)
	}
	if got.ID() != sess.ID() {
		t.Fatalf("session id = %q, want %q", got.ID(), sess.ID())
	}
	leaf, ok := got.ActiveLeafID()
	if !ok || leaf != middleID {
		t.Fatalf("active leaf = %q (ok=%v), want checkout entry %q", leaf, ok, middleID)
	}

	// 新消息挂在分叉点下，与原来的 last 记录并列构成两条分支。
	branch, err := got.AppendRecord(session.KindUser, "after checkout")
	if err != nil {
		t.Fatal(err)
	}
	if branch.Parent != middleID {
		t.Fatalf("appended parent = %q, want %q", branch.Parent, middleID)
	}
	records, err := got.Records()
	if err != nil {
		t.Fatal(err)
	}
	childrenOfMiddle := 0
	for _, record := range records {
		if record.Parent == middleID {
			childrenOfMiddle++
		}
	}
	if childrenOfMiddle != 2 {
		t.Fatalf("children of checkout entry = %d, want 2 (old branch + new branch)", childrenOfMiddle)
	}
	leaf, _ = got.ActiveLeafID()
	if leaf != branch.ID {
		t.Fatalf("active leaf after append = %q, want %q", leaf, branch.ID)
	}
}

// TestOpenOrCreateSessionWithoutAtUnchanged 验证不带 --at 时仍走原来的
// OpenSession：活动叶节点是文件顺序的最后一条记录，行为与接线前一致。
func TestOpenOrCreateSessionWithoutAtUnchanged(t *testing.T) {
	store, sess, _ := newAtTestStore(t)
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	lastID := records[len(records)-1].ID

	got, err := openOrCreateSession(store, cli.Options{Session: sess.ID()}, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	leaf, ok := got.ActiveLeafID()
	if !ok || leaf != lastID {
		t.Fatalf("active leaf = %q (ok=%v), want last record %q", leaf, ok, lastID)
	}
}

// TestOpenOrCreateSessionAtInvalidEntry 验证 --at 指向不存在的记录时返回
// 可读错误，而不是静默退回普通恢复。
func TestOpenOrCreateSessionAtInvalidEntry(t *testing.T) {
	store, sess, _ := newAtTestStore(t)
	_, err := openOrCreateSession(store, cli.Options{Session: sess.ID(), At: "missing-entry"}, "ignored")
	if err == nil {
		t.Fatal("expected error for invalid --at entry")
	}
	if !strings.Contains(err.Error(), "missing-entry") {
		t.Fatalf("error = %q, want it to mention the entry id", err)
	}
}
