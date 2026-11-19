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

func TestSearchOrdersBySessionThenLineAndHonoursLimit(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)

	writeRawSession(t, filepath.Join(dir, "older.jsonl"), []Record{
		{ID: "n1", TS: old, Kind: KindHeader, Data: json.RawMessage(`{"title":"old title"}`)},
		{ID: "n2", Parent: "n1", TS: old, Kind: KindTool, Data: json.RawMessage(`{"output":"needle in old tool output"}`)},
	})
	writeRawSession(t, filepath.Join(dir, "newer.jsonl"), []Record{
		{ID: "n1", TS: fresh, Kind: KindHeader, Data: json.RawMessage(`{"title":"new title"}`)},
		{ID: "n2", Parent: "n1", TS: fresh, Kind: KindUser, Data: json.RawMessage(`{"text":"needle in a user message"}`)},
		{ID: "n3", Parent: "n2", TS: fresh, Kind: KindTool, Data: json.RawMessage(`{"output":"NEEDLE in new tool output"}`)},
	})

	hits, err := Search(dir, "NEEDLE", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (limit): %+v", len(hits), hits)
	}
	if hits[0].ID != "newer" || hits[0].Line != 2 || hits[0].Title != "new title" {
		t.Fatalf("first hit = %+v, want newest session line 2", hits[0])
	}
	if hits[1].ID != "newer" || hits[1].Line != 3 {
		t.Fatalf("second hit = %+v, want newest session line 3", hits[1])
	}
	if !strings.Contains(hits[1].Snippet, "new tool output") {
		t.Fatalf("snippet = %q", hits[1].Snippet)
	}
	if !hits[0].TS.Equal(fresh) {
		t.Fatalf("TS = %v, want %v", hits[0].TS, fresh)
	}

	all, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search unlimited: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unlimited hits = %d, want 3", len(all))
	}
	if all[2].ID != "older" {
		t.Fatalf("last hit = %+v, want older session", all[2])
	}
}

func TestSearchNoMatchReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "empty.jsonl"), []Record{
		{ID: "n1", Kind: KindUser, Data: json.RawMessage(`"hello"`)},
	})
	hits, err := Search(dir, "absent", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("hits = %+v, want none", hits)
	}
}

func TestSearchRedactsSecretInSnippet(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "secret.jsonl"), []Record{
		{ID: "n1", Kind: KindTool, Data: json.RawMessage(`{"output":"token = 2118505078xyh1 ok"}`)},
	})
	hits, err := Search(dir, "token", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if strings.Contains(hits[0].Snippet, "2118505078xyh1") {
		t.Fatalf("secret leaked: %q", hits[0].Snippet)
	}
	if !strings.Contains(hits[0].Snippet, "[redacted:") {
		t.Fatalf("snippet not redacted: %q", hits[0].Snippet)
	}
}

// searchByFullScan is the pre-index Search, used as the semantic baseline: no
// sidecar is consulted and every transcript is parsed from scratch.
func searchByFullScan(t *testing.T, dir, query string, limit int) []Hit {
	t.Helper()
	needle := strings.TrimSpace(query)
	if needle == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	type transcript struct {
		path    string
		id      string
		title   string
		updated time.Time
		lines   []numberedRecord
	}
	files := make([]transcript, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), fileExt) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		lines, err := readNumberedRecords(path)
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), fileExt)
		records := make([]Record, 0, len(lines))
		for _, line := range lines {
			records = append(records, line.record)
		}
		meta := metaFromRecords(id, records)
		updated := meta.Updated
		if updated.IsZero() {
			if info, err := entry.Info(); err == nil {
				updated = info.ModTime()
			}
		}
		files = append(files, transcript{path: path, id: id, title: meta.Title, updated: updated, lines: lines})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].updated.Equal(files[j].updated) {
			return files[i].id > files[j].id
		}
		return files[i].updated.After(files[j].updated)
	})

	hits := make([]Hit, 0)
	for _, file := range files {
		for _, line := range file.lines {
			snippet, ok := snippetFor(line.record.Data, needle)
			if !ok {
				continue
			}
			hits = append(hits, Hit{
				Path: file.path, ID: file.id, Title: file.title,
				Line: line.number, Snippet: snippet, TS: line.record.TS,
			})
			if limit > 0 && len(hits) >= limit {
				return hits
			}
		}
	}
	return hits
}

func assertHitsEqual(t *testing.T, got, want []Hit) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hits = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].ID != want[i].ID ||
			got[i].Title != want[i].Title || got[i].Line != want[i].Line ||
			got[i].Snippet != want[i].Snippet || !got[i].TS.Equal(want[i].TS) {
			t.Errorf("hit[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSearchSidecarMatchesFullScan(t *testing.T) {
	dir := t.TempDir()
	alpha := newTestSession(t, dir, "alpha")
	appendAll(t, alpha, KindUser, KindAssistant)
	if err := alpha.Append(KindTool, map[string]string{"output": "NEEDLE in tool output"}); err != nil {
		t.Fatal(err)
	}
	beta := newTestSession(t, dir, "beta")
	if err := beta.Append(KindUser, "needle in beta"); err != nil {
		t.Fatal(err)
	}
	writeRawSession(t, filepath.Join(dir, "raw.jsonl"), []Record{
		{ID: "n1", TS: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Kind: KindUser, Data: json.RawMessage(`"a needle in a raw transcript"`)},
	})

	for _, tc := range []struct {
		query string
		limit int
	}{
		{"needle", 0},
		{"NEEDLE", 0},
		{"needle", 2},
		{"absent", 0},
	} {
		want := searchByFullScan(t, dir, tc.query, tc.limit)
		got, err := Search(dir, tc.query, tc.limit)
		if err != nil {
			t.Fatalf("Search(%q, %d): %v", tc.query, tc.limit, err)
		}
		assertHitsEqual(t, got, want)
	}

	// A second pass must use the sidecars and return identical hits.
	past := pinSidecars(t, dir)
	want := searchByFullScan(t, dir, "needle", 0)
	got, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search (cached): %v", err)
	}
	assertHitsEqual(t, got, want)
	assertSidecarsUntouched(t, dir, past)

	// A corrupt sidecar is ignored: the body is still searched in full.
	if err := os.WriteFile(alpha.path()+idxExt, []byte("broken json"), 0o644); err != nil {
		t.Fatal(err)
	}
	want = searchByFullScan(t, dir, "needle", 0)
	got, err = Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search (corrupt sidecar): %v", err)
	}
	assertHitsEqual(t, got, want)
	info, err := os.Stat(alpha.path())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loadIndex(alpha.path(), info, alpha.ID()); !ok {
		t.Fatal("corrupt sidecar was not rebuilt by Search")
	}
}

func TestSearchFallsBackWhenSidecarIsStale(t *testing.T) {
	dir := t.TempDir()
	s := newTestSession(t, dir, "task")
	if err := s.Append(KindUser, "before"); err != nil {
		t.Fatal(err)
	}
	if _, err := Search(dir, "needle", 0); err != nil {
		t.Fatal(err)
	}
	record := Record{
		ID: "n000003", Parent: "n000002",
		TS:   time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC),
		Kind: KindTool, Data: json.RawMessage(`{"output":"a new needle"}`),
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

	want := searchByFullScan(t, dir, "needle", 0)
	got, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	assertHitsEqual(t, got, want)
	if len(got) != 1 || got[0].Line != 3 {
		t.Fatalf("hits = %+v, want the externally appended line 3", got)
	}
}

func TestStoreSearchDelegates(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "s.jsonl"), []Record{
		{ID: "n1", Kind: KindUser, Data: json.RawMessage(`"find me"`)},
	})
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hits, err := store.Search("FIND", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "s" {
		t.Fatalf("hits = %+v", hits)
	}
}
