package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeOrderedSession writes a transcript whose records all share stamp, so
// metaFromRecords derives the same in-record Updated for every session here.
// mtime is set explicitly to model the real order of writes on disk, which is
// what a --resume needs when two sessions were created in the same second.
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

	// first's id sorts after second's id, so an ID-only tiebreak would place
	// it first even though second was written later.
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

	// This mirrors openOrCreateSession's --resume path: take List()[0] and
	// reopen it. It must be the session with the later mtime.
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
