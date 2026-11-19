package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func writeWorkspaceFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readWorkspaceFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func TestCheckpointCapturesHashesAndUndoRestoresOnlyManifestFiles(t *testing.T) {
	root := testWorkspace(t, map[string]string{
		"a.txt":       "old a",
		"docs/文档.txt": "old unicode",
		"other.txt":   "not in manifest",
	})
	cp, err := CaptureCheckpoint(root, []string{"docs/文档.txt", "a.txt", "a.txt"})
	if err != nil {
		t.Fatalf("CaptureCheckpoint: %v", err)
	}
	if len(cp.Files) != 2 || cp.Files[0].Path != "a.txt" || cp.Files[1].Path != "docs/文档.txt" {
		t.Fatalf("files = %+v", cp.Files)
	}
	if cp.Files[0].SHA256 != hashBytes([]byte("old a")) || string(cp.Files[1].Before) != "old unicode" {
		t.Fatalf("manifest hashes/bytes = %+v", cp.Files)
	}

	writeWorkspaceFile(t, root, "a.txt", "turn a")
	writeWorkspaceFile(t, root, "docs/文档.txt", "turn unicode")
	writeWorkspaceFile(t, root, "other.txt", "manual other")
	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatalf("CompleteCheckpoint: %v", err)
	}
	sess := newTestSession(t, t.TempDir(), "checkpoint")
	record, err := sess.RecordCheckpoint(cp)
	if err != nil {
		t.Fatalf("RecordCheckpoint: %v", err)
	}

	restored, err := UndoCheckpoint(root, cp)
	if err != nil {
		t.Fatalf("UndoCheckpoint: %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("restored = %v", restored)
	}
	if got := readWorkspaceFile(t, root, "a.txt"); got != "old a" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readWorkspaceFile(t, root, "docs/文档.txt"); got != "old unicode" {
		t.Fatalf("unicode file = %q", got)
	}
	if got := readWorkspaceFile(t, root, "other.txt"); got != "manual other" {
		t.Fatalf("out-of-manifest file was touched: %q", got)
	}
	latest, latestRecord, err := sess.LatestCheckpoint()
	if err != nil || latestRecord.ID != record.ID || len(latest.Files) != 2 {
		t.Fatalf("LatestCheckpoint = %+v, %+v, %v", latest, latestRecord, err)
	}
}

func TestUndoRefusesConflictingManualEdits(t *testing.T) {
	root := testWorkspace(t, map[string]string{"a.txt": "before"})
	cp, err := CaptureCheckpoint(root, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "a.txt", "after turn")
	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "a.txt", "manual edit")
	if _, err := UndoCheckpoint(root, cp); !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("error = %v, want ErrCheckpointConflict", err)
	}
	if got := readWorkspaceFile(t, root, "a.txt"); got != "manual edit" {
		t.Fatalf("conflicting file was overwritten: %q", got)
	}
}

func TestUndoRestoresCreatedAndDeletedFiles(t *testing.T) {
	root := testWorkspace(t, map[string]string{"gone.txt": "old gone", "keep.txt": "old keep"})
	cp, err := CaptureCheckpoint(root, []string{"gone.txt", "keep.txt", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !cp.Files[2].Absent || cp.Files[0].Absent {
		t.Fatalf("absence flags = %+v", cp.Files)
	}
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "new.txt", "created by turn")
	writeWorkspaceFile(t, root, "keep.txt", "old keep")
	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatal(err)
	}
	if !cp.Files[0].AfterAbsent || cp.Files[2].AfterSHA256 == "" {
		t.Fatalf("post-turn state = %+v", cp.Files)
	}
	restored, err := UndoCheckpoint(root, cp)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 {
		t.Fatalf("restored = %v", restored)
	}
	if got := readWorkspaceFile(t, root, "gone.txt"); got != "old gone" {
		t.Fatalf("deleted file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file still exists: %v", err)
	}
}

func TestCheckpointRejectsEscapesAndSymlinks(t *testing.T) {
	root := testWorkspace(t, map[string]string{"a.txt": "old"})
	for _, path := range []string{"../escape.txt", "/absolute/path", "", filepath.Join(root, "a.txt")} {
		if _, err := CaptureCheckpoint(root, []string{path}); err == nil {
			t.Errorf("CaptureCheckpoint(%q) unexpectedly succeeded", path)
		}
		if _, err := UndoCheckpoint(root, Checkpoint{Files: []CheckpointFile{{Path: path}}}); err == nil {
			t.Errorf("UndoCheckpoint(%q) unexpectedly succeeded", path)
		}
	}
	outside := t.TempDir()
	writeWorkspaceFile(t, outside, "secret.txt", "outside")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err == nil {
		if _, err := CaptureCheckpoint(root, []string{"link/secret.txt"}); err == nil {
			t.Error("symlinked directory was accepted")
		}
	} else {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestAbortedTurnKeepsPartialCheckpointAndUndoIsNoop(t *testing.T) {
	root := testWorkspace(t, map[string]string{"a.txt": "before"})
	cp, err := CaptureCheckpoint(root, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	cp.Partial = true
	writeWorkspaceFile(t, root, "a.txt", "partial turn")
	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatal(err)
	}
	sess := newTestSession(t, t.TempDir(), "aborted")
	if _, err := sess.RecordCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	latest, _, err := sess.LatestCheckpoint()
	if err != nil || !latest.Partial {
		t.Fatalf("partial checkpoint = %+v, %v", latest, err)
	}
	restored, err := UndoCheckpoint(root, latest)
	if err != nil || len(restored) != 1 {
		t.Fatalf("partial undo = %v, %v", restored, err)
	}
	if got := readWorkspaceFile(t, root, "a.txt"); got != "before" {
		t.Fatalf("partial turn was not undone: %q", got)
	}
}

func TestRetryForksAtCheckpointParentAndLeavesSourceUnchanged(t *testing.T) {
	dir := t.TempDir()
	sess := newTestSession(t, dir, "retry me")
	if err := sess.Append(KindUser, "first question"); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindAssistant, "first answer"); err != nil {
		t.Fatal(err)
	}
	cp, err := CaptureCheckpoint(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := sess.RecordCheckpoint(cp)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(KindAssistant, "answer after checkpoint"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatal(err)
	}

	fork, err := sess.RetryFromCheckpoint("")
	if err != nil {
		t.Fatalf("RetryFromCheckpoint: %v", err)
	}
	after, err := os.ReadFile(sess.path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source session changed during retry")
	}
	if fork.ID() == sess.ID() {
		t.Fatal("retry reused the source session id")
	}
	messages := fork.Messages()
	if len(messages) != 2 || messages[1].Content != "first answer" {
		t.Fatalf("fork messages = %+v", messages)
	}
	attempts, err := fork.Attempts()
	if err != nil || len(attempts) != 1 {
		t.Fatalf("fork attempts = %+v, %v", attempts, err)
	}
	attempt := attempts[0]
	if attempt.Reason != AttemptRetried || attempt.SourceSession != sess.ID() || attempt.SourceRecord != record.ID {
		t.Fatalf("attempt = %+v", attempt)
	}
	branches, err := Branches(filepath.Join(dir, fork.ID()+fileExt))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, branch := range branches {
		if branch.SourceSession == sess.ID() && branch.SourceRecord == record.ID {
			found = true
			if !strings.Contains(branch.Label, "retried") {
				t.Fatalf("branch label = %q", branch.Label)
			}
		}
	}
	if !found {
		t.Fatalf("tree has no retry link: %+v", branches)
	}
	if _, err := Retry(dir, sess.ID(), record.ID); err != nil {
		t.Fatalf("package Retry: %v", err)
	}
}

func TestOldJSONLRecordsRemainReadableWithoutNewKinds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	writeRawSession(t, path, []Record{
		{ID: "n1", Kind: KindHeader, Data: []byte(`{"title":""}`)},
		{ID: "n2", Parent: "n1", Kind: KindUser, Data: []byte(`{"text":"token = 2118505078xyh1"}`)},
		{ID: "n3", Parent: "n2", Kind: KindAssistant, Data: []byte(`"ok"`)},
	})
	sess, err := OpenSession(dir, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sess.Messages()); got != 2 {
		t.Fatalf("legacy messages = %d", got)
	}
	if _, _, err := sess.LatestCheckpoint(); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("LatestCheckpoint = %v, want ErrNoCheckpoint", err)
	}
	records, err := readRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	meta := metaFromRecords("legacy", records)
	if strings.Contains(meta.Title, "sk-proj") || !strings.Contains(meta.Title, "redacted") {
		t.Fatalf("lazily generated title = %q", meta.Title)
	}
}
