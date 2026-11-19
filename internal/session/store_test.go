package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// listByFullScan is the pre-index implementation, kept as the semantic
// baseline: every transcript is parsed and nothing trusts a sidecar.
func listByFullScan(t *testing.T, dir string) []Meta {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	metas := make([]Meta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(fileExt)]
		records, err := readRecords(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		meta := metaFromRecords(id, records)
		if meta.Updated.IsZero() {
			if info, err := entry.Info(); err == nil {
				meta.Updated = info.ModTime()
			}
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Updated.Equal(metas[j].Updated) {
			return metas[i].ID > metas[j].ID
		}
		return metas[i].Updated.After(metas[j].Updated)
	})
	return metas
}

func assertMetasEqual(t *testing.T, got, want []Meta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("metas = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i].ID != want[i].ID || got[i].Title != want[i].Title ||
			got[i].Turns != want[i].Turns || !got[i].Updated.Equal(want[i].Updated) {
			t.Errorf("meta[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// pinSidecars moves every .idx back in time so a rebuild is observable: the
// cache is never stat'ed for freshness, only the transcript is.
func pinSidecars(t *testing.T, dir string) time.Time {
	t.Helper()
	past := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), idxExt) {
			continue
		}
		if err := os.Chtimes(filepath.Join(dir, entry.Name()), past, past); err != nil {
			t.Fatal(err)
		}
	}
	return past
}

func assertSidecarsUntouched(t *testing.T, dir string, past time.Time) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), idxExt) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(past) {
			t.Errorf("sidecar %s was rewritten (mtime %v, want %v)", entry.Name(), info.ModTime(), past)
		}
	}
}

func TestListUsesSidecarWhenFresh(t *testing.T) {
	dir := t.TempDir()
	alpha := newTestSession(t, dir, "alpha")
	appendAll(t, alpha, KindUser, KindAssistant, KindTool)
	beta := newTestSession(t, dir, "beta")
	if err := beta.Append(KindMessage, map[string]string{"role": "assistant", "content": "hi"}); err != nil {
		t.Fatal(err)
	}
	writeRawSession(t, filepath.Join(dir, "raw.jsonl"), []Record{
		{ID: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindHeader, Data: json.RawMessage(`{"title":"raw"}`)},
		{ID: "n2", Parent: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindAssistant, Data: json.RawMessage(`"done"`)},
	})

	want := listByFullScan(t, dir)
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertMetasEqual(t, got, want)

	// The sidecar holds exactly the meta the full scan derived.
	info, err := os.Stat(alpha.path())
	if err != nil {
		t.Fatal(err)
	}
	cached, ok := loadIndex(alpha.path(), info, alpha.ID())
	if !ok || cached.Title != "alpha" || cached.Turns != 1 {
		t.Fatalf("cached meta = %+v (ok=%v), want alpha/1 turn", cached, ok)
	}

	// A second List must read the sidecars instead of reparsing transcripts.
	past := pinSidecars(t, dir)
	got, err = List(dir)
	if err != nil {
		t.Fatalf("List (cached): %v", err)
	}
	assertMetasEqual(t, got, want)
	assertSidecarsUntouched(t, dir, past)
}

func TestListFallsBackOnCorruptOrStaleSidecar(t *testing.T) {
	t.Run("garbage", func(t *testing.T) {
		dir := t.TempDir()
		s := newTestSession(t, dir, "task")
		appendAll(t, s, KindUser, KindAssistant)
		if _, err := List(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.path()+idxExt, []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		want := listByFullScan(t, dir)
		got, err := List(dir)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertMetasEqual(t, got, want)
		info, err := os.Stat(s.path())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := loadIndex(s.path(), info, s.ID()); !ok {
			t.Fatal("corrupt sidecar was not rebuilt")
		}
	})

	t.Run("wrong version", func(t *testing.T) {
		dir := t.TempDir()
		s := newTestSession(t, dir, "task")
		if _, err := List(dir); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(s.path() + idxExt)
		if err != nil {
			t.Fatal(err)
		}
		var entry indexEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			t.Fatal(err)
		}
		entry.Version++
		data, err = json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.path()+idxExt, data, 0o644); err != nil {
			t.Fatal(err)
		}
		want := listByFullScan(t, dir)
		got, err := List(dir)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertMetasEqual(t, got, want)
	})

	t.Run("stale after external append", func(t *testing.T) {
		dir := t.TempDir()
		s := newTestSession(t, dir, "task")
		if _, err := List(dir); err != nil {
			t.Fatal(err)
		}
		record := Record{
			ID: "n000002", Parent: "n000001",
			TS:   time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC),
			Kind: KindAssistant, Data: json.RawMessage(`"later reply"`),
		}
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(s.path(), os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()

		want := listByFullScan(t, dir)
		got, err := List(dir)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		assertMetasEqual(t, got, want)
		if got[0].Turns != 1 || !got[0].Updated.Equal(record.TS) {
			t.Fatalf("stale sidecar was trusted: %+v", got[0])
		}
	})
}

func TestListRefreshesSidecarAfterAppend(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "task")
	if _, err := List(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(s.path(), info, s.ID()); !ok {
		t.Fatal("initial sidecar missing")
	}

	if err := s.Append(KindAssistant, "reply"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Appends go through Session.Append, which cannot be hooked from the
	// store without changing session.go. The stat check detects the growth.
	newInfo, err := os.Stat(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(s.path(), newInfo, s.ID()); ok {
		t.Fatal("sidecar still matches a transcript that grew")
	}

	want := listByFullScan(t, dir)
	got, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	assertMetasEqual(t, got, want)
	refreshed, err := os.Stat(s.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(s.path(), refreshed, s.ID()); !ok {
		t.Fatal("sidecar was not refreshed after append")
	}
	if got[0].Turns != 1 {
		t.Fatalf("turns = %d, want 1: %+v", got[0].Turns, got[0])
	}
}

func TestDeleteRemovesSidecar(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "gone")
	if _, err := List(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path() + idxExt); err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}
	if err := Delete(dir, s.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(s.path() + idxExt); !os.IsNotExist(err) {
		t.Fatalf("sidecar survived Delete: %v", err)
	}
}
