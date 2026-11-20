package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// archiveTestEntries returns a mix of plain CJK text and raw JSON payloads so
// the round-trip covers both Entry.Content and the original json.RawMessage.
func archiveTestEntries() []Entry {
	return []Entry{
		{ID: "r1", Role: "user", Content: "目标：实现词法分析器", TS: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Data: json.RawMessage("null")},
		{ID: "r2", Role: "assistant", Content: "已改完 <tag> & \"quotes\"", Data: json.RawMessage(`{"tool_calls":[{"id":"c1"}]}`)},
		{ID: "r3", Role: "tool", Name: "shell", ToolCallID: "c1", Content: "编译中...\n失败: 未定义的符号 token", Data: json.RawMessage(`{"path":"内部/文件.go"}`)},
		{ID: "r4", Role: "system", Content: "下一步: 补测试", Data: json.RawMessage("null")},
	}
}

func TestWriteCompactionArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := archiveTestEntries()

	path, err := WriteCompactionArchive(dir, "session-001", want)
	if err != nil {
		t.Fatalf("WriteCompactionArchive: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("archive not written at %s: %v", path, err)
	}
	if wantPath := filepath.Join(dir, compactionArchiveDir, "session-001.jsonl"); path != wantPath {
		t.Fatalf("path = %q, want %q", path, wantPath)
	}

	got, err := ReadCompactionArchive(dir, path)
	if err != nil {
		t.Fatalf("ReadCompactionArchive(absolute): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("archive round-trip mismatch:\n got: %#v\nwant: %#v", got, want)
	}

	// The store-relative path stored in a ref must resolve through dir too.
	rel, err := WriteCompactionArchiveRef(dir, "session-002", want)
	if err != nil {
		t.Fatalf("WriteCompactionArchiveRef: %v", err)
	}
	gotRel, err := ReadCompactionArchive(dir, rel.Path)
	if err != nil {
		t.Fatalf("ReadCompactionArchive(relative %q): %v", rel.Path, err)
	}
	if !reflect.DeepEqual(gotRel, want) {
		t.Fatalf("relative-path round-trip mismatch:\n got: %#v\nwant: %#v", gotRel, want)
	}
}

func TestWriteCompactionArchiveByteRanges(t *testing.T) {
	dir := t.TempDir()
	messages := archiveTestEntries()

	ref, err := WriteCompactionArchiveRef(dir, "ranges", messages)
	if err != nil {
		t.Fatalf("WriteCompactionArchiveRef: %v", err)
	}
	full, err := CompactionArchivePath(dir, "ranges")
	if err != nil {
		t.Fatalf("CompactionArchivePath: %v", err)
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if ref.Bytes != info.Size() {
		t.Fatalf("ref.Bytes = %d, want file size %d", ref.Bytes, info.Size())
	}
	if len(ref.Ranges) != len(messages) {
		t.Fatalf("len(Ranges) = %d, want %d", len(ref.Ranges), len(messages))
	}

	var cursor int64
	for i, rng := range ref.Ranges {
		if rng.Start != cursor {
			t.Fatalf("range[%d].Start = %d, want contiguous %d", i, rng.Start, cursor)
		}
		if rng.End <= rng.Start {
			t.Fatalf("range[%d] is empty: [%d,%d)", i, rng.Start, rng.End)
		}
		cursor = rng.End
		if rng.ID != messages[i].ID || rng.Role != messages[i].Role {
			t.Errorf("range[%d] id/role = %q/%q, want %q/%q", i, rng.ID, rng.Role, messages[i].ID, messages[i].Role)
		}

		raw, err := ReadArchiveRange(dir, full, rng)
		if err != nil {
			t.Fatalf("ReadArchiveRange[%d]: %v", i, err)
		}
		var entry Entry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("range[%d] bytes are not one JSON entry: %v\n%s", i, err, raw)
		}
		if !reflect.DeepEqual(entry, messages[i]) {
			t.Fatalf("range[%d] recovered message mismatch:\n got: %#v\nwant: %#v", i, entry, messages[i])
		}
		// A CJK payload must survive as UTF-8, never as \uXXXX escapes.
		if i == 0 && !bytes.Contains(raw, []byte("词法分析器")) {
			t.Errorf("range[0] lost literal CJK bytes: %s", raw)
		}
	}
	if cursor != ref.Bytes {
		t.Fatalf("ranges cover %d bytes, want %d", cursor, ref.Bytes)
	}
}

func TestWriteCompactionArchivePathSafety(t *testing.T) {
	dir := t.TempDir()
	messages := archiveTestEntries()

	for _, id := range []string{"", "   ", ".", "..", "../evil", `..\evil`, "a/b", `a\b`, `x:y`, "a*b", "a?b", "a<b", "a|b"} {
		if _, err := WriteCompactionArchive(dir, id, messages); err == nil {
			t.Errorf("id %q: expected rejection, got nil error", id)
		}
	}
	// No file may appear outside the compactions directory.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.jsonl")); !os.IsNotExist(err) {
		t.Errorf("archive escaped store dir: stat err = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, compactionArchiveDir))
	if err == nil && len(entries) != 0 {
		t.Errorf("invalid ids created files: %v", entries)
	}

	// A trailing .jsonl suffix is accepted and does not duplicate the extension.
	path, err := WriteCompactionArchive(dir, "ok.jsonl", messages)
	if err != nil {
		t.Fatalf("suffixed id rejected: %v", err)
	}
	if filepath.Base(path) != "ok.jsonl" {
		t.Fatalf("path = %q, want base ok.jsonl", path)
	}
}

func TestWriteCompactionArchiveNoResidue(t *testing.T) {
	dir := t.TempDir()
	archiveDir := filepath.Join(dir, compactionArchiveDir)

	// Rewriting the same id must leave exactly one archive, no temp files.
	first := archiveTestEntries()[:1]
	second := archiveTestEntries()
	if _, err := WriteCompactionArchive(dir, "same", first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteCompactionArchive(dir, "same", second); err != nil {
		t.Fatalf("second write: %v", err)
	}
	names := archiveNames(t, archiveDir)
	if len(names) != 1 || names[0] != "same.jsonl" {
		t.Fatalf("archive dir after rewrite = %v, want [same.jsonl]", names)
	}
	got, err := ReadCompactionArchive(dir, filepath.Join(archiveDir, "same.jsonl"))
	if err != nil {
		t.Fatalf("read after rewrite: %v", err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("rewrite did not replace content")
	}

	// An encode failure must not leave a temp file behind. Invalid RawMessage
	// makes json.Encoder fail on that entry.
	bad := []Entry{{ID: "bad", Role: "user", Content: "x", Data: json.RawMessage([]byte("{broken"))}}
	if _, err := WriteCompactionArchive(dir, "bad", bad); err == nil {
		t.Fatal("expected encode error for invalid RawMessage")
	}
	for _, name := range archiveNames(t, archiveDir) {
		if strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, ".compact-") {
			t.Fatalf("encode error left temp residue: %s", name)
		}
	}
}

func TestReadCompactionArchiveRejectsEscapingPath(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"../outside.jsonl", `..\outside.jsonl`, "compactions/../../outside.jsonl"} {
		if _, err := ReadCompactionArchive(dir, p); err == nil {
			t.Errorf("path %q: expected rejection, got nil error", p)
		}
		if _, err := ReadArchiveRange(dir, p, ArchiveRange{Start: 0, End: 1}); err == nil {
			t.Errorf("range read of path %q: expected rejection, got nil error", p)
		}
	}
}

func TestCompactionArchiveNoticeAndField(t *testing.T) {
	if got := CompactionArchiveNotice(nil); got != "" {
		t.Fatalf("notice(nil) = %q, want empty", got)
	}
	ref := &ArchiveRef{Path: "compactions/session-001.jsonl", Bytes: 1234,
		Ranges: []ArchiveRange{{ID: "r1", Role: "user", Start: 0, End: 80}}}
	notice := CompactionArchiveNotice(ref)
	for _, want := range []string{"compactions/session-001.jsonl", "1234", "archive.ranges"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice missing %q: %s", want, notice)
		}
	}

	// The Archive field is additive: absent when nil, present when set.
	plain, err := json.Marshal(Compaction{Summary: "S"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "archive") {
		t.Errorf("nil Archive should be omitted: %s", plain)
	}
	withRef, err := json.Marshal(Compaction{Summary: "S", Archive: ref})
	if err != nil {
		t.Fatal(err)
	}
	var decoded Compaction
	if err := json.Unmarshal(withRef, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Archive == nil || decoded.Archive.Path != ref.Path || decoded.Archive.Bytes != ref.Bytes {
		t.Fatalf("Archive field did not round-trip: %#v", decoded.Archive)
	}
}

func TestCompactionArchiveInvisibleToList(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCompactionArchive(dir, sess.ID(), archiveTestEntries()); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	metas, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != sess.ID() {
		t.Fatalf("List = %#v, want only the session %q", metas, sess.ID())
	}
}

func archiveNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}
