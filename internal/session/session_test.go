package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestSession(t *testing.T, dir, title string) *Session {
	t.Helper()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	session, err := store.NewSession(title)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

func appendAll(t *testing.T, session *Session, kinds ...string) {
	t.Helper()
	for _, kind := range kinds {
		if err := session.Append(kind, kind+" text"); err != nil {
			t.Fatalf("Append(%q): %v", kind, err)
		}
	}
}

func writeRawSession(t *testing.T, path string, records []Record) {
	t.Helper()
	var builder strings.Builder
	for _, record := range records {
		if record.TS.IsZero() {
			record.TS = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
		}
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		builder.Write(line)
		builder.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestAppendAndRead(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "demo")

	appendAll(t, session, KindUser, KindAssistant, KindTool)

	records, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("records = %d, want 4 (header + 3)", len(records))
	}
	if records[0].Kind != KindHeader {
		t.Fatalf("first kind = %q, want header", records[0].Kind)
	}
	for i, record := range records {
		wantID := []string{"n000001", "n000002", "n000003", "n000004"}[i]
		if record.ID != wantID {
			t.Errorf("record %d id = %q, want %q", i, record.ID, wantID)
		}
		if record.TS.IsZero() {
			t.Errorf("record %d has no timestamp", i)
		}
	}
	if records[0].Parent != "" {
		t.Errorf("root parent = %q, want empty", records[0].Parent)
	}
	for i := 1; i < len(records); i++ {
		if records[i].Parent != records[i-1].ID {
			t.Errorf("record %d parent = %q, want %q", i, records[i].Parent, records[i-1].ID)
		}
	}

	messages := session.Messages()
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "user text" {
		t.Errorf("message 0 = %+v", messages[0])
	}
	if messages[2].Role != "tool" {
		t.Errorf("message 2 role = %q, want tool", messages[2].Role)
	}

	reopened, err := OpenSession(dir, session.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if got := len(reopened.Messages()); got != 3 {
		t.Fatalf("reopened messages = %d, want 3", got)
	}
	if err := reopened.Append(KindUser, "after reopen"); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	appended, err := reopened.Records()
	if err != nil {
		t.Fatalf("records after reopen: %v", err)
	}
	if last := appended[len(appended)-1]; last.Parent != "n000004" || last.ID != "n000005" {
		t.Fatalf("appended record = %+v, want id n000005 with parent n000004", last)
	}
}

func TestMessagesFollowParentLinksNotFileOrder(t *testing.T) {
	dir := t.TempDir()
	const id = "branching"
	writeRawSession(t, filepath.Join(dir, id+fileExt), []Record{
		{ID: "n000001", Kind: KindUser, Data: json.RawMessage(`"root"`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, Data: json.RawMessage(`"branch a"`)},
		{ID: "n000003", Parent: "n000001", Kind: KindUser, Data: json.RawMessage(`"branch b"`)},
	})

	session, err := OpenSession(dir, id)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	messages := session.Messages()
	if len(messages) != 2 || messages[0].Content != "root" || messages[1].Content != "branch b" {
		t.Fatalf("messages = %+v, want root then branch b", messages)
	}

	if err := session.Append(KindAssistant, "reply to b"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	records, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if last := records[len(records)-1]; last.Parent != "n000003" || last.ID != "n000004" {
		t.Fatalf("appended to %q id %q, want parent n000003 id n000004", last.Parent, last.ID)
	}
	if got := len(session.Messages()); got != 3 {
		t.Fatalf("messages = %d, want 3", got)
	}
}

func TestListFindAndDelete(t *testing.T) {
	dir := t.TempDir()
	alpha := newTestSession(t, dir, "alpha task")
	beta := newTestSession(t, dir, "beta task")
	derived := newTestSession(t, dir, "")

	if err := derived.Append(KindUser, strings.Repeat("x", 120)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 3 {
		t.Fatalf("listed %d sessions, want 3", len(metas))
	}
	if metas[0].ID != derived.ID() {
		t.Errorf("first listed = %q, want most recently updated %q", metas[0].ID, derived.ID())
	}
	for i := 1; i < len(metas); i++ {
		if metas[i-1].Updated.Before(metas[i].Updated) {
			t.Errorf("list is not sorted newest first: %v", metas)
		}
	}

	byTitle, err := Find(dir, "alpha")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(byTitle) != 1 || byTitle[0].ID != alpha.ID() {
		t.Fatalf("find alpha = %+v, want %q", byTitle, alpha.ID())
	}
	byID, err := Find(dir, beta.ID())
	if err != nil {
		t.Fatalf("Find id: %v", err)
	}
	if len(byID) != 1 || byID[0].ID != beta.ID() {
		t.Fatalf("find by id = %+v, want %q", byID, beta.ID())
	}

	derivedMeta := metas[0]
	if want := 60; len([]rune(derivedMeta.Title)) != want {
		t.Errorf("derived title has %d runes, want %d (%q)", len([]rune(derivedMeta.Title)), want, derivedMeta.Title)
	}

	if err := Delete(dir, alpha.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := OpenSession(dir, alpha.ID()); err == nil {
		t.Fatal("OpenSession of a deleted session succeeded")
	}
	if err := Delete(dir, "../escape"); err == nil {
		t.Fatal("Delete accepted a path traversal id")
	}
}

func TestForkIsolation(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "fork me")
	if err := session.Append(KindUser, "one"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := session.Append(KindAssistant, "two"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	first := session.Messages()[0].ID

	fork, err := Fork(dir, session.ID(), first)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if fork.ID() == session.ID() {
		t.Fatal("fork reused the original session id")
	}
	if got := fork.Messages(); len(got) != 1 || got[0].Content != "one" {
		t.Fatalf("fork messages = %+v, want just 'one'", got)
	}
	if got := len(session.Messages()); got != 2 {
		t.Fatalf("original messages = %d, want 2", got)
	}

	if err := fork.Append(KindAssistant, "forked reply"); err != nil {
		t.Fatalf("fork append: %v", err)
	}
	if got := len(fork.Messages()); got != 2 {
		t.Fatalf("fork messages after append = %d, want 2", got)
	}
	if got := len(session.Messages()); got != 2 {
		t.Fatalf("original grew to %d messages after fork write", got)
	}
	if _, err := Fork(dir, session.ID(), "nope"); err == nil {
		t.Fatal("Fork of an unknown entry succeeded")
	}
}

func TestWriterStillRecords(t *testing.T) {
	dir := t.TempDir()
	writer, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := writer.Write(KindUser, "hello from the writer"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Write(KindAssistant, map[string]string{"content": "hi"}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(writer.Session().Messages()); got != 2 {
		t.Fatalf("messages = %d, want 2", got)
	}
	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("listed %d sessions, want 1", len(metas))
	}
	if metas[0].Title != "hello from the writer" {
		t.Errorf("title = %q", metas[0].Title)
	}
	if metas[0].Turns != 1 {
		t.Errorf("turns = %d, want 1", metas[0].Turns)
	}
}

func TestCompactKeepsTailAndSummaries(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "compact")

	if err := session.Append(KindUser, "build the parser"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := session.Append(KindAssistant, map[string]any{
		"role":    "assistant",
		"content": "next steps: add the lexer",
		"toolCalls": []any{
			map[string]any{"function": map[string]any{
				"name":      "write",
				"arguments": `{"path":"parser.go"}`,
			}},
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := session.Append(KindTool, map[string]string{
		"name":   "bash",
		"output": "error: undefined: token",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := session.Append(KindUser, "continue"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := session.Append(KindAssistant, "done"); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if got := len(session.Messages()); got != 5 {
		t.Fatalf("messages before compaction = %d, want 5", got)
	}
	if err := session.Compact(2); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	messages := session.Messages()
	if len(messages) != 3 {
		t.Fatalf("messages after compaction = %d, want 3 (summary + 2)", len(messages))
	}
	summary := messages[0]
	if summary.Role != "system" {
		t.Fatalf("summary role = %q, want system", summary.Role)
	}
	for _, want := range []string{"build the parser", "parser.go", "error: undefined: token", "Next steps"} {
		if !strings.Contains(summary.Content, want) {
			t.Errorf("summary missing %q:\n%s", want, summary.Content)
		}
	}
	if messages[1].Content != "continue" || messages[2].Content != "done" {
		t.Fatalf("tail = %+v, want continue then done", messages[1:])
	}

	records, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	compactions := 0
	for _, record := range records {
		if record.Kind == KindCompaction {
			compactions++
		}
	}
	if compactions != 1 {
		t.Fatalf("compaction records = %d, want 1", compactions)
	}

	// A reopened session sees the same compacted view.
	reopened, err := OpenSession(dir, session.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if got := reopened.Messages(); len(got) != 3 || got[0].Role != "system" {
		t.Fatalf("reopened messages = %+v", got)
	}

	// Compacting a history that already fits is a no-op.
	before, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if err := session.Compact(3); err != nil {
		t.Fatalf("Compact no-op: %v", err)
	}
	after, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("no-op compaction appended %d record(s)", len(after)-len(before))
	}
}

func TestCompactRejectsNegativeKeep(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "x")
	if err := session.Compact(-1); err == nil {
		t.Fatal("Compact(-1) succeeded")
	}
}

// The in-memory index must make steady-state appends O(1) in JSON parsing: one
// full read when it is built, none afterwards. The counting hook verifies the
// behaviour directly instead of relying on a fragile timing threshold.
func TestAppendRecordReusesIDIndexWithoutFullReread(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "indexed")

	reads := 0
	session.readRecordsFn = func(path string) ([]Record, error) {
		reads++
		return readRecords(path)
	}
	// newTestSession wrote the header before the hook was attached; start with
	// a cold index so the first append below builds it through the hook.
	session.invalidateAppendCacheAfterRewrite()

	const n = 64
	last := "n000001" // header minted by newTestSession
	for i := 1; i <= n; i++ {
		record, err := session.AppendRecord(KindUser, fmt.Sprintf("message %d", i))
		if err != nil {
			t.Fatalf("AppendRecord(%d): %v", i, err)
		}
		if want := fmt.Sprintf("n%06d", i+1); record.ID != want {
			t.Fatalf("record %d id = %q, want %q", i, record.ID, want)
		}
		if record.Parent != last {
			t.Fatalf("record %d parent = %q, want %q", i, record.Parent, last)
		}
		last = record.ID
	}
	if reads != 1 {
		t.Fatalf("full reads during %d appends = %d, want 1", n, reads)
	}

	records, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != n+1 {
		t.Fatalf("records = %d, want %d", len(records), n+1)
	}
	for i, record := range records {
		if want := fmt.Sprintf("n%06d", i+1); record.ID != want {
			t.Errorf("record %d id = %q, want %q", i, record.ID, want)
		}
		if i > 0 && record.Parent != records[i-1].ID {
			t.Errorf("record %d parent = %q, want %q", i, record.Parent, records[i-1].ID)
		}
	}
	if reads != 1 {
		t.Fatalf("Records() must stay an explicit full read via readRecords (reads = %d)", reads)
	}
}

func TestAppendRecordIDChainAfterReopen(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "reopen")
	for i := 0; i < 10; i++ {
		if err := session.Append(KindUser, fmt.Sprintf("m%d", i)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	last := session.Messages()[len(session.Messages())-1].ID

	reopened, err := OpenSession(dir, session.ID())
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reads := 0
	reopened.readRecordsFn = func(path string) ([]Record, error) {
		reads++
		return readRecords(path)
	}
	for i := 1; i <= 5; i++ {
		record, err := reopened.AppendRecord(KindAssistant, fmt.Sprintf("reply %d", i))
		if err != nil {
			t.Fatalf("AppendRecord after reopen: %v", err)
		}
		if record.Parent != last {
			t.Fatalf("record %d parent = %q, want %q", i, record.Parent, last)
		}
		if want := fmt.Sprintf("n%06d", 11+i); record.ID != want {
			t.Fatalf("record %d id = %q, want %q", i, record.ID, want)
		}
		last = record.ID
	}
	if reads != 1 {
		t.Fatalf("full reads after reopen = %d, want 1", reads)
	}
}

// Replacing the file (temp + rename, as a rewrite/compaction would) must make
// the next append rebuild its index: no id may collide with the new records.
func TestAppendRecordDetectsReplacedFile(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "replaced")
	if err := session.Append(KindUser, "local"); err != nil {
		t.Fatalf("Append: %v", err)
	}
	reads := 0
	session.readRecordsFn = func(path string) ([]Record, error) {
		reads++
		return readRecords(path)
	}
	// Warm the index through the counting hook before replacing the file.
	session.invalidateAppendCacheAfterRewrite()
	if _, err := session.AppendRecord(KindUser, "warm"); err != nil {
		t.Fatalf("warm AppendRecord: %v", err)
	}
	if reads != 1 {
		t.Fatalf("full reads after warm append = %d, want 1", reads)
	}

	path := session.path()
	tmp := path + ".new"
	writeRawSession(t, tmp, []Record{
		{ID: "n000001", Kind: KindHeader, Data: json.RawMessage(`{"title":"external","created":"2026-01-01T00:00:00Z"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, Data: json.RawMessage(`"external a"`)},
		{ID: "n000009", Parent: "n000002", Kind: KindUser, Data: json.RawMessage(`"external b"`)},
	})
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove old file: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename replacement: %v", err)
	}

	// An active id absent from the rewritten file exercises the same fallback
	// the old full-read path used: link to the last record in file order.
	session.active = "gone"
	record, err := session.AppendRecord(KindAssistant, "after replacement")
	if err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	if reads != 2 {
		t.Fatalf("full reads = %d, want 2 (initial build + rebuild)", reads)
	}
	if record.ID != "n000003" {
		t.Errorf("id = %q, want n000003 (smallest unused id)", record.ID)
	}
	if record.Parent != "n000009" {
		t.Errorf("parent = %q, want n000009", record.Parent)
	}
	records, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != 4 || records[2].ID != "n000009" || records[3].Parent != "n000009" {
		t.Fatalf("records after replacement = %+v", records)
	}
}

func TestAppendRecordDetectsInPlaceRewrite(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "in-place")
	appendAll(t, session, KindUser, KindAssistant, KindUser)
	reads := 0
	session.readRecordsFn = func(path string) ([]Record, error) {
		reads++
		return readRecords(path)
	}
	session.invalidateAppendCacheAfterRewrite()
	if _, err := session.AppendRecord(KindUser, "warm"); err != nil {
		t.Fatalf("warm AppendRecord: %v", err)
	}
	if reads != 1 {
		t.Fatalf("full reads after warm append = %d, want 1", reads)
	}

	// Rewrite the same inode with a shorter file; a stale index would mint an
	// id that no longer matches the on-disk transcript.
	writeRawSession(t, session.path(), []Record{
		{ID: "n000001", Kind: KindHeader, Data: json.RawMessage(`{"title":"short","created":"2026-01-01T00:00:00Z"}`)},
		{ID: "n000004", Kind: KindUser, Data: json.RawMessage(`"only record"`)},
	})
	record, err := session.AppendRecord(KindAssistant, "after rewrite")
	if err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	if reads != 2 {
		t.Fatalf("full reads = %d, want 2", reads)
	}
	if record.ID != "n000002" {
		t.Errorf("id = %q, want n000002", record.ID)
	}
	records, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != 3 || records[2].ID != "n000002" {
		t.Fatalf("records after in-place rewrite = %+v", records)
	}
}

func TestForkAppendContinuesIDChain(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "fork chain")
	appendAll(t, session, KindUser, KindAssistant)

	fork, err := session.Fork("n000002")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	before, err := fork.Records()
	if err != nil {
		t.Fatalf("fork Records: %v", err)
	}
	last := before[len(before)-1].ID
	for i := 0; i < 3; i++ {
		wantID := nextID(before)
		record, err := fork.AppendRecord(KindAssistant, fmt.Sprintf("forked %d", i))
		if err != nil {
			t.Fatalf("fork AppendRecord: %v", err)
		}
		if record.ID != wantID {
			t.Fatalf("fork record id = %q, want %q", record.ID, wantID)
		}
		if record.Parent != last {
			t.Fatalf("fork record parent = %q, want %q", record.Parent, last)
		}
		last = record.ID
		before = append(before, record)
	}
	after, err := fork.Records()
	if err != nil {
		t.Fatalf("fork Records after append: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("fork records = %d, want %d", len(after), len(before))
	}
	for i := 1; i < len(after); i++ {
		if after[i].Parent != after[i-1].ID {
			t.Errorf("fork record %d parent = %q, want %q", i, after[i].Parent, after[i-1].ID)
		}
	}
	original, err := session.Records()
	if err != nil {
		t.Fatalf("original Records: %v", err)
	}
	if len(original) != 3 {
		t.Fatalf("original grew to %d records", len(original))
	}
}

func TestAppendRecordPreservesUTF8Payload(t *testing.T) {
	session := newTestSession(t, t.TempDir(), "utf8")
	text := "你好，世界 🌍 " + strings.Repeat("很长的中文内容", 2000)
	record, err := session.AppendRecord(KindUser, text)
	if err != nil {
		t.Fatalf("AppendRecord: %v", err)
	}
	records, err := session.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	last := records[len(records)-1]
	if last.ID != record.ID {
		t.Fatalf("id = %q, want %q", last.ID, record.ID)
	}
	if got := textOf(last.Data); got != text {
		t.Fatalf("payload round trip lost %d bytes", len(text)-len(got))
	}
}
