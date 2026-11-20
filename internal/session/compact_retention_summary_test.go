package session

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestCompactionRestoresOldSummaryText covers the compaction+retention
// interaction: a compaction record is stamped RetainAlways, so when a later
// compaction drops it, retainedForCompaction re-emits it. entryKind maps the
// compaction's system Entry to KindSummary, but the re-emitted record keeps the
// Compaction payload (which has a "summary" key, not "text"/"content"). Without
// "summary" in messageEntry's KindSummary keys, the restored message loses all
// its text and resurfaces as an empty system entry.
func TestCompactionRestoresOldSummaryText(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewSession("chain")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	first := s.Messages()
	var firstSummary string
	for _, e := range first {
		if e.Role == "system" && e.Content != "" {
			firstSummary = e.Content
			break
		}
	}
	if firstSummary == "" {
		t.Fatal("first compaction produced no summary")
	}

	// Grow the conversation past the first compaction and compact again. The
	// cut now drops the first compaction's system Entry, which is flagged for
	// retention because KindCompaction is stamped RetainAlways.
	for i := 5; i < 9; i++ {
		if err := s.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(1); err != nil {
		t.Fatalf("second Compact: %v", err)
	}

	// The re-emitted summary must still carry the original text, not an empty
	// system message. There are now two system entries: the new compaction's
	// summary and the restored one; the earlier one must survive verbatim.
	var sawRestored, sawEmpty bool
	for _, e := range s.Messages() {
		if e.Role != "system" {
			continue
		}
		if e.Content == firstSummary {
			sawRestored = true
		}
		if e.Content == "" {
			sawEmpty = true
		}
	}
	if sawEmpty {
		t.Fatalf("a system entry was re-emitted with empty content: %+v", s.Messages())
	}
	if !sawRestored {
		t.Fatalf("first compaction summary text was lost after re-emission: %+v", s.Messages())
	}
}

// TestMessageEntrySummaryKey documents that a KindSummary carrying the
// canonical "summary" payload key projects onto the same Entry as one carrying
// "text" — the invariant retainedForCompaction relies on.
func TestMessageEntrySummaryKey(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"summary": "kept summary"})
	entry, ok := messageEntry(Record{Kind: KindSummary, Data: data})
	if !ok {
		t.Fatal("KindSummary with summary key did not project to an Entry")
	}
	if entry.Role != "system" || entry.Content != "kept summary" {
		t.Fatalf("entry = %+v, want system/kept summary", entry)
	}
}
