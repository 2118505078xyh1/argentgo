package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSearchStripsUppercaseJSONLExtension covers a case-inconsistency bug:
// Search matches the ".jsonl" suffix case-insensitively but used a
// case-sensitive strings.TrimSuffix to derive the session id. A hand-copied
// transcript named "SESSION.JSONL" therefore produced Hit.ID "SESSION.JSONL",
// which no longer resolves through Store.OpenSession. The id must be stripped
// by length, the same way Container.List does, preserving the original case.
func TestSearchStripsUppercaseJSONLExtension(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"UPPER.JSONL", "Mixed.Jsonl", "lower.jsonl"} {
		record := Record{
			ID:   "n000001",
			Kind: KindUser,
			Data: json.RawMessage(`{"role":"user","content":"needle in ` + name + `"}`),
		}
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(line, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	hits, err := Search(dir, "needle", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(hits))
	}
	wantIDs := map[string]bool{"UPPER": true, "Mixed": true, "lower": true}
	for _, hit := range hits {
		if !wantIDs[hit.ID] {
			t.Errorf("hit ID = %q, want one of %v", hit.ID, wantIDs)
		}
		if strings.EqualFold(hit.ID, "UPPER") && hit.ID != "UPPER" {
			t.Errorf("case not preserved: %q", hit.ID)
		}
	}
}
