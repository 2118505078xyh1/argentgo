package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func validRepairLine(t *testing.T, id, parent, kind string) string {
	t.Helper()
	record := Record{
		ID:     id,
		Parent: parent,
		TS:     time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
		Kind:   kind,
		Data:   json.RawMessage(`{"role":"user","content":"hi"}`),
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func writeRepairFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertRepairFileBytes(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("file bytes = %q, want %q", data, want)
	}
}

func assertNoRepairTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("staged repair file left behind: %s", entry.Name())
		}
	}
}

func TestValidateAndRepairAllValidFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "valid.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	second := validRepairLine(t, "n000002", "n000001", KindMessage)
	content := first + "\n" + second + "\n"
	writeRepairFile(t, path, content)

	for _, line := range []string{first, second} {
		if kind, detail := decodeLine(line); kind != DecodeOK {
			t.Fatalf("decodeLine(%q) = %v (%s), want DecodeOK", line, kind, detail)
		}
	}
	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.Path != path || report.PhysicalLines != 2 || report.KeptLines != 2 ||
		report.TornTail || report.Repaired {
		t.Fatalf("report = %+v, want 2/2 lines, no torn tail, no repair", report)
	}
	assertRepairFileBytes(t, path, content)
}

func TestValidateAndRepairTornLastLineWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "torn.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	second := validRepairLine(t, "n000002", "n000001", KindMessage)
	content := first + "\n" + second + "\n" + `{"id":"tor`
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, false)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if !report.TornTail || report.Repaired || report.PhysicalLines != 3 || report.KeptLines != 2 {
		t.Fatalf("report = %+v, want torn tail with 2 kept lines and no repair", report)
	}
	assertRepairFileBytes(t, path, content)
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairTornLastLineWithRepair(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "recover")
	appendAll(t, session, KindMessage, KindAssistant)
	before, err := session.Records()
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := os.ReadFile(session.path())
	if err != nil {
		t.Fatal(err)
	}
	writeRepairFile(t, session.path(), string(prefix)+`{"kind":"entry"`)

	report, err := ValidateAndRepair(session.path(), true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if !report.TornTail || !report.Repaired || report.KeptLines != len(before) {
		t.Fatalf("report = %+v, want repaired torn tail with %d kept lines", report, len(before))
	}
	assertRepairFileBytes(t, session.path(), string(prefix))

	after, err := session.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("records = %d, want %d after repair", len(after), len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Kind != before[i].Kind {
			t.Errorf("record[%d] = %q/%q, want %q/%q",
				i, after[i].ID, after[i].Kind, before[i].ID, before[i].Kind)
		}
	}
	if viaRead, err := readRecords(session.path()); err != nil || len(viaRead) != len(before) {
		t.Fatalf("readRecords after repair = %d records, err %v", len(viaRead), err)
	}
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairTornMiddleLineIsHardCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "middle.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	third := validRepairLine(t, "n000003", "n000001", KindMessage)
	content := first + "\n" + `{"id":"tor` + "\n" + third + "\n"
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, true)
	if !errors.Is(err, ErrHardCorruption) {
		t.Fatalf("ValidateAndRepair error = %v, want ErrHardCorruption", err)
	}
	if report.TornTail || report.Repaired {
		t.Fatalf("report = %+v, want no torn tail and no repair", report)
	}
	assertRepairFileBytes(t, path, content)
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairSchemaErrorOnLastLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	content := first + "\n" + `{"id":"x","kind":123}` + "\n"
	writeRepairFile(t, path, content)

	if kind, detail := decodeLine(`{"id":"x","kind":123}`); kind != DecodeSchema {
		t.Fatalf("decodeLine(schema) = %v (%s), want DecodeSchema", kind, detail)
	}
	report, err := ValidateAndRepair(path, true)
	if !errors.Is(err, ErrHardCorruption) {
		t.Fatalf("ValidateAndRepair error = %v, want ErrHardCorruption", err)
	}
	if report.TornTail || report.Repaired {
		t.Fatalf("report = %+v, want no torn tail and no repair", report)
	}
	assertRepairFileBytes(t, path, content)
	assertNoRepairTemps(t, dir)
}

func TestValidateAndRepairLegacyRecordWithoutID(t *testing.T) {
	line := `{"kind":"message","ts":"2026-11-19T12:00:00Z","data":{"role":"user","content":"legacy"}}`
	if kind, detail := decodeLine(line); kind != DecodeOK {
		t.Fatalf("decodeLine(legacy) = %v (%s), want DecodeOK", kind, detail)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	writeRepairFile(t, path, line+"\n")

	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.TornTail || report.Repaired || report.KeptLines != 1 {
		t.Fatalf("report = %+v, want one valid line and no repair", report)
	}
	assertRepairFileBytes(t, path, line+"\n")
	records, err := readRecords(path)
	if err != nil || len(records) != 1 || records[0].ID != "n000001" {
		t.Fatalf("readRecords = %+v, err %v; want one synthesized id", records, err)
	}
}

func TestValidateAndRepairAppendsMissingTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unterminated.jsonl")
	first := validRepairLine(t, "n000001", "", KindHeader)
	second := validRepairLine(t, "n000002", "n000001", KindMessage)
	content := first + "\n" + second
	writeRepairFile(t, path, content)

	report, err := ValidateAndRepair(path, true)
	if err != nil {
		t.Fatalf("ValidateAndRepair: %v", err)
	}
	if report.TornTail || !report.Repaired || report.KeptLines != 2 {
		t.Fatalf("report = %+v, want a newline repair of two valid lines", report)
	}
	assertRepairFileBytes(t, path, content+"\n")

	// Listing-style validation never writes, even for a missing newline.
	writeRepairFile(t, path, content)
	report, err = ValidateAndRepair(path, false)
	if err != nil {
		t.Fatalf("ValidateAndRepair(repair=false): %v", err)
	}
	if report.Repaired || report.TornTail {
		t.Fatalf("report = %+v, want no write when repair is false", report)
	}
	assertRepairFileBytes(t, path, content)
}

func TestAtomicPublishJSONLWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows rename and directory-sync path")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "publish.jsonl")
	writeRepairFile(t, path, "stale\n")
	data := []byte(validRepairLine(t, "n000001", "", KindHeader) + "\n")

	// Directory Sync returns EINVAL on Windows; it must not fail publication.
	if err := atomicPublishJSONL(path, data); err != nil {
		t.Fatalf("atomicPublishJSONL: %v (directory Sync EINVAL must be ignored)", err)
	}
	assertRepairFileBytes(t, path, string(data))
	assertNoRepairTemps(t, dir)
}

func TestOpenSessionRejectsHardCorruptTranscript(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(t, dir, "corrupt")
	content := validRepairLine(t, "n000001", "", KindHeader) + "\n" +
		`{"id":"x","kind":123}` + "\n"
	writeRepairFile(t, session.path(), content)

	_, err := OpenSession(dir, session.ID())
	if err == nil {
		t.Fatal("OpenSession accepted a hard-corrupt transcript")
	}
	if !errors.Is(err, ErrHardCorruption) || !strings.Contains(err.Error(), "resume session") {
		t.Fatalf("OpenSession error = %v, want wrapped ErrHardCorruption", err)
	}
	assertRepairFileBytes(t, session.path(), content)
	assertNoRepairTemps(t, dir)
}

func TestListSkipsHardCorruptTranscript(t *testing.T) {
	dir := t.TempDir()
	good := newTestSession(t, dir, "good")
	bad := newTestSession(t, dir, "bad")
	content := `{"id":"x","kind":123}` + "\n"
	writeRepairFile(t, bad.path(), content)

	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 || metas[0].ID != good.ID() {
		t.Fatalf("List metas = %+v, want only %s", metas, good.ID())
	}
	found, err := Find(dir, "")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(found) != 1 || found[0].ID != good.ID() {
		t.Fatalf("Find metas = %+v, want only %s", found, good.ID())
	}
	assertRepairFileBytes(t, bad.path(), content)
	assertNoRepairTemps(t, dir)
}
