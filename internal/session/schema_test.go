package session

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testRecord(id, kind, data string) Record {
	return Record{ID: id, Kind: kind, Data: json.RawMessage(data)}
}

func schemaHeader(t *testing.T, version int) Record {
	t.Helper()
	record, err := SchemaRecord(version)
	if err != nil {
		t.Fatalf("SchemaRecord(%d): %v", version, err)
	}
	return record
}

func TestSchemaRecordBuildsHeader(t *testing.T) {
	record := schemaHeader(t, CurrentSchemaVersion)
	if record.Kind != KindSchema {
		t.Fatalf("kind = %q, want %q", record.Kind, KindSchema)
	}
	var payload SchemaPayload
	if err := json.Unmarshal(record.Data, &payload); err != nil {
		t.Fatalf("unmarshal schema data: %v", err)
	}
	if payload.Version != CurrentSchemaVersion {
		t.Fatalf("version = %d, want %d", payload.Version, CurrentSchemaVersion)
	}
	if got := string(record.Data); got != `{"version":1}` {
		t.Fatalf("data = %s, want %s", got, `{"version":1}`)
	}
}

func TestSchemaRecordRejectsNegativeVersion(t *testing.T) {
	if _, err := SchemaRecord(-1); err == nil {
		t.Fatal("SchemaRecord(-1) succeeded, want error")
	}
}

// TestMigrateRecordsLegacyV0AddsHeader: a transcript with no schema header is
// v0 and gains exactly one header on top; nothing else changes.
func TestMigrateRecordsLegacyV0AddsHeader(t *testing.T) {
	records := []Record{
		testRecord("a", KindHeader, `{"title":"t"}`),
		testRecord("b", KindUser, `{"text":"hello"}`),
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if len(got) != len(records)+1 {
		t.Fatalf("len = %d, want %d", len(got), len(records)+1)
	}
	if got[0].Kind != KindSchema {
		t.Fatalf("first kind = %q, want %q", got[0].Kind, KindSchema)
	}
	var payload SchemaPayload
	if err := json.Unmarshal(got[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if payload.Version != CurrentSchemaVersion {
		t.Fatalf("version = %d, want %d", payload.Version, CurrentSchemaVersion)
	}
	if !reflect.DeepEqual(got[1:], records) {
		t.Fatalf("original records changed: got %#v", got[1:])
	}
}

// TestMigrateRecordsExplicitV1Unchanged: a transcript already at the current
// version passes through byte-for-byte.
func TestMigrateRecordsExplicitV1Unchanged(t *testing.T) {
	records := []Record{
		schemaHeader(t, 1),
		testRecord("b", KindUser, `{"text":"hi"}`),
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if !reflect.DeepEqual(got, records) {
		t.Fatalf("v1 transcript was modified:\ngot  %#v\nwant %#v", got, records)
	}
}

// TestMigrateRecordsIdempotent: migrating twice yields the same result as once,
// for both a legacy (v0) and an already-current (v1) input.
func TestMigrateRecordsIdempotent(t *testing.T) {
	inputs := [][]Record{
		{testRecord("a", KindUser, `{"text":"x"}`)},
		{schemaHeader(t, 1), testRecord("a", KindUser, `{"text":"x"}`)},
	}
	for i, records := range inputs {
		once, err := MigrateRecords(records)
		if err != nil {
			t.Fatalf("input %d: first pass: %v", i, err)
		}
		twice, err := MigrateRecords(once)
		if err != nil {
			t.Fatalf("input %d: second pass: %v", i, err)
		}
		if !reflect.DeepEqual(twice, once) {
			t.Fatalf("input %d: not idempotent:\nonce  %#v\ntwice %#v", i, once, twice)
		}
	}
}

// TestMigrateRecordsV0HeaderUpdatedInPlace: an explicit v0 header is bumped in
// place rather than duplicated.
func TestMigrateRecordsV0HeaderUpdatedInPlace(t *testing.T) {
	records := []Record{
		schemaHeader(t, 0),
		testRecord("b", KindUser, `{"text":"x"}`),
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if len(got) != len(records) {
		t.Fatalf("len = %d, want %d (no duplicate header)", len(got), len(records))
	}
	var payload SchemaPayload
	if err := json.Unmarshal(got[0].Data, &payload); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if payload.Version != CurrentSchemaVersion {
		t.Fatalf("version = %d, want %d", payload.Version, CurrentSchemaVersion)
	}
	if got[1].ID != "b" {
		t.Fatalf("second record id = %q, want b", got[1].ID)
	}
}

// TestMigrateRecordsNewerVersionRejected: a header from a newer build must be a
// clear error, never a silent downgrade.
func TestMigrateRecordsNewerVersionRejected(t *testing.T) {
	header := schemaHeader(t, 999)
	records := []Record{header, testRecord("b", KindUser, `{"text":"x"}`)}
	_, err := MigrateRecords(records)
	if err == nil {
		t.Fatal("MigrateRecords succeeded on a newer version, want error")
	}
	if !errors.Is(err, ErrNewerSchemaVersion) {
		t.Fatalf("err = %v, want ErrNewerSchemaVersion", err)
	}
	if !strings.Contains(err.Error(), "999") {
		t.Fatalf("err = %q, want it to mention the offending version", err)
	}
}

// TestMigrateRecordsEmpty: an empty transcript is a no-op.
func TestMigrateRecordsEmpty(t *testing.T) {
	got, err := MigrateRecords(nil)
	if err != nil {
		t.Fatalf("MigrateRecords(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
	got, err = MigrateRecords([]Record{})
	if err != nil {
		t.Fatalf("MigrateRecords(empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

// TestMigrateRecordsPreservesUnknownKinds: kinds are open; an unrecognised kind
// survives migration untouched.
func TestMigrateRecordsPreservesUnknownKinds(t *testing.T) {
	unknown := testRecord("u", "future_kind_from_newer_build", `{"opaque":[1,2,3]}`)
	records := []Record{
		testRecord("a", KindUser, `{"text":"x"}`),
		unknown,
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	found := false
	for _, record := range got {
		if record.ID == "u" {
			found = true
			if !reflect.DeepEqual(record, unknown) {
				t.Fatalf("unknown kind changed: got %#v, want %#v", record, unknown)
			}
		}
	}
	if !found {
		t.Fatal("unknown kind record was dropped")
	}
}

// TestMigrateRecordsCJK: non-ASCII payloads are carried through unchanged.
func TestMigrateRecordsCJK(t *testing.T) {
	cjk := testRecord("b", KindUser, `{"text":"你好，世界 — 日本語・한국어 🚀"}`)
	records := []Record{
		testRecord("a", KindHeader, `{"title":"会话标题"}`),
		cjk,
	}
	got, err := MigrateRecords(records)
	if err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if len(got) != len(records)+1 {
		t.Fatalf("len = %d, want %d", len(got), len(records)+1)
	}
	if !reflect.DeepEqual(got[1:], records) {
		t.Fatalf("CJK records changed:\ngot  %#v\nwant %#v", got[1:], records)
	}
	if !strings.Contains(string(got[2].Data), "你好，世界") {
		t.Fatalf("CJK text lost: %s", got[2].Data)
	}
}

// TestMigrateRecordsDoesNotMutateInput: the caller's slice is never modified.
func TestMigrateRecordsDoesNotMutateInput(t *testing.T) {
	original := []Record{
		testRecord("a", KindUser, `{"text":"x"}`),
		testRecord("b", KindAssistant, `{"text":"y"}`),
	}
	snapshot := append([]Record(nil), original...)
	if _, err := MigrateRecords(original); err != nil {
		t.Fatalf("MigrateRecords: %v", err)
	}
	if !reflect.DeepEqual(original, snapshot) {
		t.Fatalf("input mutated:\ngot  %#v\nwant %#v", original, snapshot)
	}
}

// TestMigrateRecordsMalformedHeader: a broken header is reported, not silently
// treated as v0.
func TestMigrateRecordsMalformedHeader(t *testing.T) {
	records := []Record{testRecord("h", KindSchema, `not json`)}
	if _, err := MigrateRecords(records); err == nil {
		t.Fatal("MigrateRecords succeeded on a malformed header, want error")
	}
}

// TestMigrateRecordsConflictingHeaders: two headers disagreeing on the version
// are reported rather than guessed at.
func TestMigrateRecordsConflictingHeaders(t *testing.T) {
	records := []Record{
		schemaHeader(t, 1),
		schemaHeader(t, 0),
	}
	if _, err := MigrateRecords(records); err == nil {
		t.Fatal("MigrateRecords succeeded on conflicting headers, want error")
	}
}
