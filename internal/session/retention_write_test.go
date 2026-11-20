package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func stampedRecordIDs(records []Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.ID)
	}
	return out
}

func TestAppendWithRetentionStoresHint(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "retention write")
	payload := []byte(`{"role":"user","content":"remember me","count":3}`)

	record, err := sess.AppendWithRetention(KindMessage, payload, RetainAlways)
	if err != nil {
		t.Fatalf("AppendWithRetention: %v", err)
	}
	if got := GetRetention(record.Data); got != RetainAlways {
		t.Fatalf("returned record retention = %v, want RetainAlways", got)
	}

	records, err := sess.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	last := records[len(records)-1]
	if last.ID != record.ID {
		t.Fatalf("stored id = %q, want %q", last.ID, record.ID)
	}
	if got := GetRetention(last.Data); got != RetainAlways {
		t.Fatalf("stored retention = %v, want RetainAlways", got)
	}
	if got, want := normalizedObject(t, last.Data), normalizedObject(t, payload); !reflect.DeepEqual(got, want) {
		t.Fatalf("other fields changed: got %v, want %v", got, want)
	}
}

// TestAppendWithRetentionExplicitHintBeatsKindDefault covers the case where a
// caller asks for a hint that differs from the kind's automatic default. The
// explicit value must survive AppendRecord's StampIfImportant step; otherwise
// e.g. a summary marked in_thread silently becomes always and leaks into child
// sessions against the caller's intent.
func TestStampIfImportantPreservesExplicitHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want Retention
	}{
		{"explicit different value", `{"content":"x","_retention":"in_thread"}`, RetainInThread},
		{"explicit default", `{"content":"x","_retention":"default"}`, RetainDefault},
		{"unrecognised value", `{"content":"x","_retention":"weird"}`, RetainDefault},
		{"null is no hint", `{"content":"x","_retention":null}`, RetainAlways},
		{"non-string is no hint", `{"content":"x","_retention":7}`, RetainAlways},
		{"no hint", `{"content":"x"}`, RetainAlways},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := StampIfImportant(KindSummary, []byte(tc.data))
			if got := GetRetention(out); got != tc.want {
				t.Fatalf("StampIfImportant retention = %v, want %v (data %s)", got, tc.want, out)
			}
		})
	}
}

func TestAppendWithRetentionExplicitHintBeatsKindDefault(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want Retention
	}{
		{KindSummary, RetainInChildren},  // automatic default: RetainAlways
		{KindCheckpoint, RetainInThread}, // automatic default: RetainAlways
		{KindTitle, RetainAlways},        // automatic default: RetainInChildren
	} {
		t.Run(tc.kind+"/"+tc.want.String(), func(t *testing.T) {
			sess := newTestSession(t, t.TempDir(), "explicit hint")
			record, err := sess.AppendWithRetention(tc.kind,
				[]byte(`{"content":"payload"}`), tc.want)
			if err != nil {
				t.Fatalf("AppendWithRetention: %v", err)
			}
			if got := GetRetention(record.Data); got != tc.want {
				t.Fatalf("returned record retention = %v, want %v", got, tc.want)
			}
			records, err := sess.Records()
			if err != nil {
				t.Fatal(err)
			}
			if got := GetRetention(records[len(records)-1].Data); got != tc.want {
				t.Fatalf("stored retention = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAppendWithRetentionRejectsBadPayload(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "retention bad payload")
	before, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendWithRetention(KindMessage, []byte(`"bare string"`), RetainAlways); err == nil {
		t.Error("non-object payload was accepted, want error")
	}
	if _, err := sess.AppendWithRetention(KindMessage, bytes.Repeat([]byte("x"), retentionDataLimit+1), RetainAlways); err == nil {
		t.Error("oversized payload was accepted, want error")
	}
	after, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed appends wrote records: before %d, after %d", len(before), len(after))
	}
}

func TestAppendWithRetentionAllowsEmptyEnvelope(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "retention empty")
	for i, data := range [][]byte{nil, {}, []byte("null")} {
		record, err := sess.AppendWithRetention(KindMessage, data, RetainInChildren)
		if err != nil {
			t.Fatalf("case %d AppendWithRetention(%q): %v", i, data, err)
		}
		if got := GetRetention(record.Data); got != RetainInChildren {
			t.Fatalf("case %d retention = %v, want RetainInChildren", i, got)
		}
	}
}

func TestRetentionFromKindPolicy(t *testing.T) {
	tests := []struct {
		kind string
		want Retention
	}{
		{KindCheckpoint, RetainAlways},
		{KindSummary, RetainAlways},
		{KindCompaction, RetainAlways},
		{" CHECKPOINT ", RetainAlways},
		{"Summary", RetainAlways},
		{KindTitle, RetainInChildren},
		{"title", RetainInChildren},
		{KindMessage, RetainDefault},
		{KindUser, RetainDefault},
		{KindAssistant, RetainDefault},
		{KindTool, RetainDefault},
		{KindHeader, RetainDefault},
		{"attempt", RetainDefault},
		{"progress", RetainDefault},
		{"stats", RetainDefault},
		{"usage", RetainDefault},
		{"", RetainDefault},
		{"unknown-kind", RetainDefault},
	}
	for _, tt := range tests {
		if got := RetentionFromKind(tt.kind); got != tt.want {
			t.Errorf("RetentionFromKind(%q) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestStampIfImportantMarksOnlyImportantKinds(t *testing.T) {
	data := []byte(`{"content":"hello 世界","n":1}`)

	for _, kind := range []string{KindCheckpoint, KindSummary, KindCompaction} {
		out := StampIfImportant(kind, data)
		if bytes.Equal(out, data) {
			t.Errorf("StampIfImportant(%q) left data unstamped", kind)
		}
		if got := GetRetention(out); got != RetainAlways {
			t.Errorf("StampIfImportant(%q) retention = %v, want RetainAlways", kind, got)
		}
		if got, want := normalizedObject(t, out), normalizedObject(t, data); !reflect.DeepEqual(got, want) {
			t.Errorf("StampIfImportant(%q) changed payload: got %v, want %v", kind, got, want)
		}
	}
	if out := StampIfImportant(KindTitle, data); GetRetention(out) != RetainInChildren {
		t.Errorf("title retention = %v, want RetainInChildren", GetRetention(out))
	}

	for _, kind := range []string{KindMessage, KindUser, KindAssistant, KindTool, "progress", "stats", "unknown"} {
		if out := StampIfImportant(kind, data); !bytes.Equal(out, data) {
			t.Errorf("StampIfImportant(%q) changed default-kind bytes: %q, want %q", kind, out, data)
		}
	}

	// An important kind with a non-object payload keeps its bytes: a hint must
	// never cost the payload itself.
	bare := []byte(`"just text"`)
	if out := StampIfImportant(KindCheckpoint, bare); !bytes.Equal(out, bare) {
		t.Errorf("non-object payload changed: %q, want %q", out, bare)
	}
}

func TestAppendRecordAutoStampsImportantKinds(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "auto stamp")
	if _, err := sess.AppendRecord(KindCheckpoint, map[string]any{"prompt": "p"}); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetTitle("T"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(KindCompaction, Compaction{Summary: "S"}); err != nil {
		t.Fatal(err)
	}
	plain := map[string]any{"content": "plain 消息"}
	if _, err := sess.AppendRecord(KindMessage, plain); err != nil {
		t.Fatal(err)
	}

	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	byKind := make(map[string]Record, len(records))
	for _, record := range records {
		byKind[record.Kind] = record
	}
	for _, tt := range []struct {
		kind string
		want Retention
	}{
		{KindCheckpoint, RetainAlways},
		{KindCompaction, RetainAlways},
		{KindTitle, RetainInChildren},
		{KindMessage, RetainDefault},
	} {
		if got := GetRetention(byKind[tt.kind].Data); got != tt.want {
			t.Errorf("%s retention = %v, want %v", tt.kind, got, tt.want)
		}
	}
	wantPlain, err := encodePayload(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(byKind[KindMessage].Data, wantPlain) {
		t.Fatalf("default-kind data changed: got %q, want %q", byKind[KindMessage].Data, wantPlain)
	}
}

func TestRetentionSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "reopen")
	if _, err := sess.AppendWithRetention(KindMessage, []byte(`{"role":"user","content":"in thread"}`), RetainInThread); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(KindSummary, map[string]any{"text": "durable summary"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(KindTitle, TitleRecord{Title: "T"}); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenSession(dir, sess.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	records, err := reopened.Records()
	if err != nil {
		t.Fatal(err)
	}
	records = withoutSchemaHeader(records)
	if got := GetRetention(records[1].Data); got != RetainInThread {
		t.Errorf("reopened message retention = %v, want RetainInThread", got)
	}
	if got := GetRetention(records[2].Data); got != RetainAlways {
		t.Errorf("reopened summary retention = %v, want RetainAlways", got)
	}
	if got := GetRetention(records[3].Data); got != RetainInChildren {
		t.Errorf("reopened title retention = %v, want RetainInChildren", got)
	}
}

func TestSelectorsSeeStampedRecords(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "selectors")
	seed, err := sess.AppendWithRetention(KindMessage, []byte(`{"role":"user","content":"seed children"}`), RetainInChildren)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := sess.AppendWithRetention(KindMessage, []byte(`{"role":"assistant","content":"survive compaction"}`), RetainInThread)
	if err != nil {
		t.Fatal(err)
	}
	both, err := sess.AppendWithRetention(KindMessage, []byte(`{"role":"user","content":"both"}`), RetainAlways)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(KindMessage, map[string]any{"role": "user", "content": "plain"}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := sess.AppendRecord(KindCheckpoint, map[string]any{"prompt": "cp"})
	if err != nil {
		t.Fatal(err)
	}
	title, err := sess.AppendRecord(KindTitle, TitleRecord{Title: "ttl"})
	if err != nil {
		t.Fatal(err)
	}

	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stampedRecordIDs(SelectForCompaction(records, 0)), []string{thread.ID, both.ID, checkpoint.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SelectForCompaction = %v, want %v", got, want)
	}
	if got, want := stampedRecordIDs(SelectForChildren(records)), []string{seed.ID, both.ID, checkpoint.ID, title.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SelectForChildren = %v, want %v", got, want)
	}
}

func TestCompactionRestoresStampedRecord(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "compact stamped")
	const pinned = "pinned context: 世界"
	if _, err := sess.AppendWithRetention(KindMessage, []byte(`{"role":"user","content":"`+pinned+`"}`), RetainAlways); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sess.Append(KindMessage, map[string]any{"role": "user", "content": fmt.Sprintf("filler %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.Compact(1); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	records, err := sess.Records()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	restored := false
	for _, record := range records {
		// The compaction summary also quotes the dropped text; only count
		// conversation records, not the summary record itself.
		if record.Kind == KindCompaction || !strings.Contains(string(record.Data), pinned) {
			continue
		}
		count++
		// entryKind maps a "user" role back to KindUser when the compactor
		// re-emits the flagged record, so the restored copy has that kind.
		if record.Kind == KindUser {
			restored = true
		}
		if got := GetRetention(record.Data); got != RetainAlways {
			t.Errorf("re-emitted record lost its hint: %v", got)
		}
	}
	if count != 2 || !restored {
		t.Fatalf("pinned records after compaction = %d (restored=%v), want original + re-emitted (2)", count, restored)
	}

	reopened, err := OpenSession(dir, sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range reopened.Messages() {
		if entry.Content == pinned {
			found = true
		}
	}
	if !found {
		t.Fatalf("reopened conversation lost the pinned message: %+v", reopened.Messages())
	}
}

func TestAppendWithRetentionRedactsAndPreservesCJK(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "redaction")
	const secret = "sk-ant-not-a-real-key-1234567890"
	payload, err := json.Marshal(map[string]string{"text": "token = " + secret + " 你好 世界"})
	if err != nil {
		t.Fatal(err)
	}

	record, err := sess.AppendWithRetention(KindSummary, payload, RetainAlways)
	if err != nil {
		t.Fatalf("AppendWithRetention: %v", err)
	}
	raw := string(record.Data)
	if strings.Contains(raw, secret) {
		t.Fatalf("stored summary still contains the secret: %s", raw)
	}
	if !strings.Contains(raw, "[redacted:") {
		t.Fatalf("stored summary was not redacted: %s", raw)
	}
	if got := GetRetention(record.Data); got != RetainAlways {
		t.Fatalf("retention = %v, want RetainAlways", got)
	}
	var decoded map[string]string
	if err := json.Unmarshal(record.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded["text"], "你好 世界") {
		t.Fatalf("CJK text was lost: %q", decoded["text"])
	}
}
