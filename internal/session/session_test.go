package session

import (
	"encoding/json"
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
