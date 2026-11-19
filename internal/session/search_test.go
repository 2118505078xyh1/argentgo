package session

import (
	"encoding/json"
	"path/filepath"
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
