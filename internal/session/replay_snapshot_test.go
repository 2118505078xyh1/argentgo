package session

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"
)

func snapshotRecord(id, kind string, data any) Record {
	b, _ := json.Marshal(data)
	return Record{ID: id, Kind: kind, TS: time.Now(), Data: b}
}

func validSnapshotPayload(body string) RequestSnapshotPayload {
	b := []byte(body)
	return RequestSnapshotPayload{Version: 1, Protocol: "p", Model: "m", Body: b, BodySHA256: sha256.Sum256(b)}
}

func TestReplaySnapshotFromPath(t *testing.T) {
	snap := validSnapshotPayload(`{"raw":1}`)
	path := []Record{
		snapshotRecord("1", KindUser, "hi"),
		snapshotRecord("2", KindRequestSnapshot, snap),
	}
	got := ReplaySnapshotFromPath(path)
	if got == nil || got.SnapshotID != "2" {
		t.Fatalf("no snapshot found: %+v", got)
	}
	if got.HasLaterReply || got.HasCompaction {
		t.Fatal("fresh snapshot with no later records must be replayable")
	}
}

func TestReplaySnapshotInvalidatedByReply(t *testing.T) {
	snap := validSnapshotPayload(`{"raw":1}`)
	path := []Record{
		snapshotRecord("1", KindRequestSnapshot, snap),
		snapshotRecord("2", KindAssistant, "already answered"),
	}
	got := ReplaySnapshotFromPath(path)
	if got == nil || !got.HasLaterReply {
		t.Fatalf("later assistant reply not detected: %+v", got)
	}
}

func TestReplaySnapshotInvalidatedByCompaction(t *testing.T) {
	snap := validSnapshotPayload(`{"raw":1}`)
	path := []Record{
		snapshotRecord("1", KindRequestSnapshot, snap),
		snapshotRecord("2", KindCompaction, Compaction{}),
	}
	got := ReplaySnapshotFromPath(path)
	if got == nil || !got.HasCompaction {
		t.Fatalf("later compaction not detected: %+v", got)
	}
}

func TestReplaySnapshotSkipsInvalid(t *testing.T) {
	bad := RequestSnapshotPayload{Version: 1, Body: []byte("x"), BodySHA256: [32]byte{1}}
	good := validSnapshotPayload(`{"raw":2}`)
	path := []Record{
		snapshotRecord("1", KindRequestSnapshot, bad),
		snapshotRecord("2", KindRequestSnapshot, good),
	}
	got := ReplaySnapshotFromPath(path)
	if got == nil || got.SnapshotID != "2" || string(got.Payload.Body) != `{"raw":2}` {
		t.Fatalf("invalid snapshot not skipped: %+v", got)
	}
}

func TestReplaySnapshotNone(t *testing.T) {
	if got := ReplaySnapshotFromPath([]Record{{ID: "1", Kind: KindUser}}); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}
