package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDecodeRecordEquivalentToUnmarshal(t *testing.T) {
	line := `{"id":"n1","kind":"user","ts":"2026-01-01T00:00:00Z","data":{"text":"hi"}}`
	var want Record
	if err := json.Unmarshal([]byte(line), &want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := DecodeRecord(line)
	if err != nil || !ok {
		t.Fatalf("DecodeRecord: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got.Record, want) {
		t.Fatalf("record = %+v, want %+v", got.Record, want)
	}
	if got.Kind != EventUser {
		t.Fatalf("kind = %v, want EventUser", got.Kind)
	}
	if got.CorrelationID != "n1" {
		t.Fatalf("correlation = %q, want n1", got.CorrelationID)
	}
}

func TestDecodeRecordBlankAndMalformed(t *testing.T) {
	if _, ok, err := DecodeRecord("   "); ok || err != nil {
		t.Fatalf("blank: ok=%v err=%v", ok, err)
	}
	if _, ok, err := DecodeRecord("{not json"); ok || err == nil {
		t.Fatalf("malformed: ok=%v err=%v", ok, err)
	}
}

func TestEventKindTaggedUnion(t *testing.T) {
	cases := map[string]EventKind{
		KindHeader: EventHeader, KindUser: EventUser, "prompt": EventUser,
		KindAssistant: EventAssistant, "model": EventAssistant,
		KindTool: EventTool, "tool_result": EventTool,
		KindCompaction: EventCompaction, KindSummary: EventSummary,
		"stats": EventOther, "future_thing": EventUnknown,
	}
	for raw, want := range cases {
		if got := eventKindOf(raw); got != want {
			t.Errorf("eventKindOf(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestCorrelationIDFromPayload(t *testing.T) {
	rec := Record{ID: "n9", Kind: KindTool, Data: json.RawMessage(`{"toolCallId":"call-7"}`)}
	if got := correlationIDOf(rec); got != "call-7" {
		t.Fatalf("correlation = %q, want call-7", got)
	}
	rec.Data = json.RawMessage(`{}`)
	if got := correlationIDOf(rec); got != "n9" {
		t.Fatalf("fallback correlation = %q, want n9", got)
	}
}
