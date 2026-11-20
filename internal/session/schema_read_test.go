package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// This file wires schema migration into the shared read path and guards the
// behaviour end to end: legacy transcripts gain a header on read, explicit
// versions are respected, and a newer version is rejected rather than guessed
// at.

// withoutSchemaHeader drops a leading, read-time schema header so the existing
// tree/chain tests can keep asserting on the transcript's own records. The
// injected header is metadata with no parent and an empty id; it never joins
// the id/parent chain.
func withoutSchemaHeader(records []Record) []Record {
	if len(records) > 0 && records[0].Kind == KindSchema {
		return records[1:]
	}
	return records
}

// withoutSchemaBranches is the Branch counterpart of withoutSchemaHeader.
func withoutSchemaBranches(branches []Branch) []Branch {
	out := make([]Branch, 0, len(branches))
	for _, b := range branches {
		if b.Kind != KindSchema {
			out = append(out, b)
		}
	}
	return out
}

// TestReadRecordsLegacyV0GainsHeader: a v0 transcript (no schema header) read
// through the shared reader comes back upgraded to v1, with the original
// records preserved after the injected metadata header.
func TestReadRecordsLegacyV0GainsHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "n000001", Kind: KindHeader, Data: []byte(`{"title":"legacy"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, Data: []byte(`{"text":"hello"}`)},
	})

	records, err := readRecords(path)
	if err != nil {
		t.Fatalf("readRecords: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 (schema header + 2)", len(records))
	}
	if records[0].Kind != KindSchema {
		t.Fatalf("first kind = %q, want %q", records[0].Kind, KindSchema)
	}
	// The injected header is metadata with an empty id: it never enters the
	// parent chain and must not consume an id slot used by appends.
	if records[0].ID != "" {
		t.Fatalf("injected schema header id = %q, want empty", records[0].ID)
	}
	if records[1].ID != "n000001" || records[2].ID != "n000002" {
		t.Fatalf("original records moved: %+v", records[1:])
	}
	if records[2].Data == nil || string(records[2].Data) != `{"text":"hello"}` {
		t.Fatalf("original payload changed: %s", records[2].Data)
	}
}

// TestReadRecordsExplicitV1Unchanged: a transcript that already carries an
// explicit v1 schema header is returned unchanged (no duplicate header).
func TestReadRecordsExplicitV1Unchanged(t *testing.T) {
	schema, err := SchemaRecord(CurrentSchemaVersion)
	if err != nil {
		t.Fatalf("SchemaRecord: %v", err)
	}
	schema.ID = "s1"
	path := filepath.Join(t.TempDir(), "explicit.jsonl")
	writeRawSession(t, path, []Record{
		schema,
		{ID: "n000001", Kind: KindHeader, Data: []byte(`{"title":"explicit"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, Data: []byte(`"hi"`)},
	})

	records, err := readRecords(path)
	if err != nil {
		t.Fatalf("readRecords: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3 (no extra header)", len(records))
	}
	if records[0].Kind != KindSchema || records[0].ID != "s1" {
		t.Fatalf("first record = %+v, want original schema header", records[0])
	}
}

// TestReadRecordsNewerVersionErrors: a transcript from a newer build is a
// clear error, not a silent downgrade. readRecords must propagate ErrNewerSchemaVersion.
func TestReadRecordsNewerVersionErrors(t *testing.T) {
	schema, err := SchemaRecord(CurrentSchemaVersion + 10)
	if err != nil {
		t.Fatalf("SchemaRecord: %v", err)
	}
	schema.ID = "s1"
	path := filepath.Join(t.TempDir(), "newer.jsonl")
	writeRawSession(t, path, []Record{
		schema,
		{ID: "n000001", Kind: KindHeader, Data: []byte(`{"title":"future"}`)},
	})

	if _, err := readRecords(path); !errors.Is(err, ErrNewerSchemaVersion) {
		t.Fatalf("readRecords error = %v, want ErrNewerSchemaVersion", err)
	}
}

// TestMigratedSessionBranchesAndMessages: after a v0 transcript is migrated on
// read, the conversation view is unaffected — messages follow the real parent
// links and the schema header never appears as a message.
func TestMigratedSessionBranchesAndMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate-view.jsonl")
	stamp := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	writeRawSession(t, path, []Record{
		{ID: "h", TS: stamp, Kind: KindHeader, Data: []byte(`{"title":"view"}`)},
		{ID: "u1", Parent: "h", TS: stamp, Kind: KindUser, Data: []byte(`"question"`)},
		{ID: "a1", Parent: "u1", TS: stamp, Kind: KindAssistant, Data: []byte(`"answer"`)},
	})

	branches, err := Branches(path)
	if err != nil {
		t.Fatalf("Branches: %v", err)
	}
	// The injected schema header is metadata: it shows up as a branch but never
	// as a message, and the real records keep their ids/parents/depths.
	sawSchema := false
	for _, b := range branches {
		if b.Kind == KindSchema {
			sawSchema = true
		}
	}
	if !sawSchema {
		t.Fatal("Branches did not include the migrated schema header")
	}
	for _, b := range branches {
		if b.ID == "u1" && (b.Parent != "h" || b.Kind != KindUser) {
			t.Fatalf("u1 branch = %+v", b)
		}
	}

	// Messages walks from the active leaf and skips metadata records.
	session := &Session{dir: filepath.Dir(path), id: "migrate-view", active: "a1"}
	messages := session.Messages()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v", messages)
	}
}
