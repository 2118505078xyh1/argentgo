package session

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func newSnapshotSession(t *testing.T) *Session {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("snapshot test")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestAppendRequestSnapshotRoundTrip(t *testing.T) {
	sess := newSnapshotSession(t)
	body := []byte(`{"messages":[{"content":"你好 😀"}]}`)
	rec, err := AppendRequestSnapshot(sess, RequestSnapshotPayload{
		Protocol: "anthropic-messages",
		Model:    "m",
		Body:     body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != KindRequestSnapshot {
		t.Fatalf("kind = %q, want %q", rec.Kind, KindRequestSnapshot)
	}

	var loaded RequestSnapshotPayload
	if err := json.Unmarshal(rec.Data, &loaded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Body, body) {
		t.Fatalf("body mismatch: got %q, want %q", loaded.Body, body)
	}
	if !loaded.valid() {
		t.Fatal("snapshot failed hash validation")
	}
}

func TestRequestSnapshotDoesNotEnterConversation(t *testing.T) {
	sess := newSnapshotSession(t)
	if err := sess.Append(KindUser, json.RawMessage(`"hello"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendRequestSnapshot(sess, RequestSnapshotPayload{
		Protocol: "openai-chat-completions", Model: "m", Body: []byte(`{"secret":"x"}`),
	}); err != nil {
		t.Fatal(err)
	}
	msgs := sess.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1 (snapshot must be ignored)", len(msgs))
	}
	encoded, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatal("snapshot body leaked into conversation view")
	}
}

func TestRequestSnapshotsExtractsValidOnly(t *testing.T) {
	sess := newSnapshotSession(t)
	good := []byte(`{"ok":true}`)
	if _, err := AppendRequestSnapshot(sess, RequestSnapshotPayload{Protocol: "p", Model: "m", Body: good}); err != nil {
		t.Fatal(err)
	}
	// 一条 hash 不匹配的记录，应被 valid() 拒绝并跳过。
	broken := RequestSnapshotPayload{Version: 1, Protocol: "p", Model: "m", Body: []byte("tampered")}
	if _, err := sess.Append(KindRequestSnapshot, broken); err != nil {
		t.Fatal(err)
	}
	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	got := RequestSnapshots(records)
	if len(got) != 1 {
		t.Fatalf("snapshots = %d, want 1 (malformed skipped)", len(got))
	}
	if !bytes.Equal(got[0].Body, good) {
		t.Fatalf("body = %q, want %q", got[0].Body, good)
	}
}

func TestRequestSnapshotRejectsTamperedBody(t *testing.T) {
	p := RequestSnapshotPayload{Version: 1, Body: []byte("original"), BodySHA256: [32]byte{1}}
	if p.valid() {
		t.Fatal("tampered snapshot must fail validation")
	}
}
