package security

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newPendingTestStore(t *testing.T) *PendingStore {
	t.Helper()
	return NewPendingStore(t.TempDir())
}

func pendingAt(id, created string) PendingApproval {
	return PendingApproval{
		ID:         id,
		Session:    "sess-1",
		Tool:       "bash",
		ArgsDigest: PendingArgsDigest(`{"command":"ls"}`),
		Created:    created,
		Reason:     "operator requested",
	}
}

func TestPendingStoreAddListRoundtrip(t *testing.T) {
	store := newPendingTestStore(t)
	if err := store.Add(pendingAt("a", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d items, want 1", len(got))
	}
	want := pendingAt("a", "2026-01-01T00:00:01Z")
	if got[0] != want {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got[0], want)
	}
}

func TestPendingStoreListSortedByCreated(t *testing.T) {
	store := newPendingTestStore(t)
	// 乱序插入；List 必须按 Created 升序排列。
	for _, p := range []PendingApproval{
		pendingAt("c", "2026-01-01T00:00:03Z"),
		pendingAt("a", "2026-01-01T00:00:01Z"),
		pendingAt("b", "2026-01-01T00:00:02Z"),
	} {
		if err := store.Add(p); err != nil {
			t.Fatalf("Add(%s): %v", p.ID, err)
		}
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d items, want 3", len(got))
	}
	for i, wantID := range []string{"a", "b", "c"} {
		if got[i].ID != wantID {
			t.Errorf("item %d ID = %q, want %q", i, got[i].ID, wantID)
		}
	}
}

func TestPendingStoreResolveRemovesAndRecordsDecision(t *testing.T) {
	store := newPendingTestStore(t)
	for _, p := range []PendingApproval{
		pendingAt("a", "2026-01-01T00:00:01Z"),
		pendingAt("b", "2026-01-01T00:00:02Z"),
	} {
		if err := store.Add(p); err != nil {
			t.Fatalf("Add(%s): %v", p.ID, err)
		}
	}
	if err := store.Resolve("sess-1", "a", true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("after Resolve got %+v, want only b", got)
	}

	// 一行式决定记录已追加到哈希命名的决定文件中。
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var decisionPath string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".decisions.jsonl") {
			decisionPath = filepath.Join(store.dir, entry.Name())
		}
	}
	if decisionPath == "" {
		t.Fatal("no decisions file found")
	}
	data, err := os.ReadFile(decisionPath)
	if err != nil {
		t.Fatalf("ReadFile decisions: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("decisions has %d lines, want 1: %q", len(lines), data)
	}
	var rec pendingDecision
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("decode decision: %v", err)
	}
	if rec.ID != "a" || !rec.Granted || rec.TS == "" {
		t.Errorf("decision = %+v, want id=a granted=true ts set", rec)
	}
}

func TestPendingStoreResolveUnknownIDIsNoop(t *testing.T) {
	store := newPendingTestStore(t)
	if err := store.Add(pendingAt("a", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Resolve("sess-1", "missing", false); err != nil {
		t.Fatalf("Resolve unknown id should be a no-op, got: %v", err)
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("unknown-id resolve changed the store: %+v", got)
	}
}

func TestPendingStoreClearEmpties(t *testing.T) {
	store := newPendingTestStore(t)
	for _, p := range []PendingApproval{
		pendingAt("a", "2026-01-01T00:00:01Z"),
		pendingAt("b", "2026-01-01T00:00:02Z"),
	} {
		if err := store.Add(p); err != nil {
			t.Fatalf("Add(%s): %v", p.ID, err)
		}
	}
	if err := store.Clear("sess-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("after Clear got %d items, want 0", len(got))
	}
	// 清空一个已经为空的会话也是可以的。
	if err := store.Clear("sess-1"); err != nil {
		t.Fatalf("second Clear: %v", err)
	}
}

func TestPendingStoreSkipsMalformedLines(t *testing.T) {
	store := newPendingTestStore(t)
	if err := store.Add(pendingAt("good", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	path := store.sessionPath("sess-1")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	extra, err := json.Marshal(pendingAt("manual", "2026-01-01T00:00:02Z"))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	junk := []byte("{not json\n\n{\"id\":\"\"}\n" + string(extra) + "\n")
	if _, err := f.Write(junk); err != nil {
		t.Fatalf("Write junk: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List should tolerate malformed lines: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d items, want 2: %+v", len(got), got)
	}
	if got[0].ID != "good" || got[1].ID != "manual" {
		t.Errorf("got IDs %q,%q, want good,manual", got[0].ID, got[1].ID)
	}
}

func TestPendingStoreRedactsSecrets(t *testing.T) {
	store := newPendingTestStore(t)
	const reasonToken = "sk-ant-abcdefghijklmnopqrstuvwxyz1234567890"
	const toolToken = "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	p := pendingAt("a", "2026-01-01T00:00:01Z")
	p.Reason = "please use " + reasonToken + " to continue"
	p.Tool = toolToken
	if err := store.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}
	data, err := os.ReadFile(store.sessionPath("sess-1"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	onDisk := string(data)
	for _, secret := range []string{reasonToken, toolToken} {
		if strings.Contains(onDisk, secret) {
			t.Errorf("secret %q leaked to disk: %s", secret, onDisk)
		}
	}
	// 脱敏占位符存在，证明该字段确实被写入了。
	if !strings.Contains(onDisk, "[redacted:") {
		t.Errorf("expected a redaction placeholder on disk: %s", onDisk)
	}
}

func TestPendingStoreFilenameSafety(t *testing.T) {
	store := newPendingTestStore(t)
	const session = "../../escape/with/slashes\\and\\backslashes"
	p := pendingAt("a", "2026-01-01T00:00:01Z")
	p.Session = session
	if err := store.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("store dir has %d entries, want 1: %+v", len(entries), entries)
	}
	name := entries[0].Name()
	base := filepath.Base(name)
	if base != name || strings.ContainsAny(name, "/\\") {
		t.Errorf("filename %q contains path separators", name)
	}
	if !strings.HasSuffix(name, ".json") || len(name) != len("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json") {
		t.Errorf("filename %q is not a 64-hex hash plus .json", name)
	}
	// 哈希使原始 session 字符串的查找保持稳定。
	got, err := store.List(session)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("List returned %+v, want the stored item", got)
	}
}

func TestPendingStoreCapEnforcement(t *testing.T) {
	store := newPendingTestStore(t)
	for i := 0; i < maxPendingPerSession+10; i++ {
		p := pendingAt(fmt.Sprintf("p%02d", i), fmt.Sprintf("2026-01-01T00:00:%02dZ", i))
		if err := store.Add(p); err != nil {
			t.Fatalf("Add(%s): %v", p.ID, err)
		}
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != maxPendingPerSession {
		t.Fatalf("List returned %d items, want cap %d", len(got), maxPendingPerSession)
	}
	if got[0].ID != "p10" || got[len(got)-1].ID != "p59" {
		t.Errorf("cap kept IDs %q..%q, want p10..p59", got[0].ID, got[len(got)-1].ID)
	}
}

func TestPendingStoreConcurrentAccess(t *testing.T) {
	store := newPendingTestStore(t)
	const workers = 8
	const iterations = 20
	var wg sync.WaitGroup
	errCh := make(chan error, workers*iterations*3)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				id := fmt.Sprintf("w%d-i%d", w, i)
				if err := store.Add(pendingAt(id, fmt.Sprintf("2026-01-01T00:00:%02dZ", i))); err != nil {
					errCh <- err
					continue
				}
				if _, err := store.List("sess-1"); err != nil {
					errCh <- err
				}
				if err := store.Resolve("sess-1", id, i%2 == 0); err != nil {
					errCh <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent operation failed: %v", err)
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("final List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("all requests were resolved, but %d remain", len(got))
	}
	// 每个决定都记录在有界的 JSONL 日志中。
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".decisions.jsonl") {
			data, err := os.ReadFile(filepath.Join(store.dir, entry.Name()))
			if err != nil {
				t.Fatalf("ReadFile decisions: %v", err)
			}
			lines := strings.Count(string(data), "\n")
			if lines != workers*iterations {
				t.Errorf("decision log has %d lines, want %d", lines, workers*iterations)
			}
		}
	}
}

func TestPendingStoreCreatesNonexistentDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "deeper", "pending")
	// 直接构造，使创建目录的责任落在 Add 而非构造函数上。
	store := &PendingStore{dir: dir}
	if err := store.Add(pendingAt("a", "2026-01-01T00:00:01Z")); err != nil {
		t.Fatalf("Add with missing dir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("Add did not create dir %q: %v", dir, err)
	}
	got, err := store.List("sess-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List returned %d items, want 1", len(got))
	}
}

func TestPendingArgsDigestCanonicalization(t *testing.T) {
	tests := []struct {
		name      string
		a         string
		b         string
		wantEqual bool
	}{
		{"key order", `{"b":2,"a":1}`, `{ "a": 1, "b": 2 }`, true},
		{"whitespace", "  ls   -la  ", "ls -la", true},
		{"distinct values", `{"a":1}`, `{"a":2}`, false},
		{"number spelling", `{"n":1}`, `{"n":1.0}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			da, db := PendingArgsDigest(tt.a), PendingArgsDigest(tt.b)
			if equal := da == db; equal != tt.wantEqual {
				t.Errorf("digest(%q)=%s digest(%q)=%s equal=%v, want %v", tt.a, da, tt.b, db, equal, tt.wantEqual)
			}
			if len(da) != 64 {
				t.Errorf("digest length = %d, want 64 hex chars", len(da))
			}
		})
	}
}
