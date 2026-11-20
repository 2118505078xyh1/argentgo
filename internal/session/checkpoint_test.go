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

func TestCheckpointBackWalksRecentCheckpointsOnActiveBranch(t *testing.T) {
	sess := newTestSession(t, t.TempDir(), "multi")
	first, err := sess.RecordCheckpoint(Checkpoint{Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sess.RecordCheckpoint(Checkpoint{Prompt: "second"})
	if err != nil {
		t.Fatal(err)
	}

	latest, record, err := sess.CheckpointBack(1)
	if err != nil || record.ID != second.ID || latest.Prompt != "second" {
		t.Fatalf("CheckpointBack(1) = %+v, %+v, %v", latest, record, err)
	}
	older, record, err := sess.CheckpointBack(2)
	if err != nil || record.ID != first.ID || older.Prompt != "first" {
		t.Fatalf("CheckpointBack(2) = %+v, %+v, %v", older, record, err)
	}
	if _, _, err := sess.CheckpointBack(3); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("CheckpointBack(3) error = %v, want ErrNoCheckpoint", err)
	}
	if _, _, err := sess.CheckpointBack(0); err == nil {
		t.Fatal("CheckpointBack(0) should reject invalid steps")
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

// TestUndoRefusesWhenAnyManifestFileAdvanced covers the git-less equivalent of
// "HEAD moved past the checkpoint base": a later change to any manifest file
// aborts the whole undo, and no file -- including untouched ones -- is written.
func TestUndoRefusesWhenAnyManifestFileAdvanced(t *testing.T) {
	root := testWorkspace(t, map[string]string{"a.txt": "before a", "b.txt": "before b"})
	cp, err := CaptureCheckpoint(root, []string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "a.txt", "after a")
	writeWorkspaceFile(t, root, "b.txt", "after b")
	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatal(err)
	}
	// A user commit advances only b.txt after the checkpoint.
	writeWorkspaceFile(t, root, "b.txt", "committed by user")

	restored, err := UndoCheckpoint(root, cp)
	if !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("error = %v, want ErrCheckpointConflict", err)
	}
	if len(restored) != 0 {
		t.Fatalf("restored = %v, want none: undo must be all-or-nothing", restored)
	}
	if got := readWorkspaceFile(t, root, "a.txt"); got != "after a" {
		t.Fatalf("a.txt was partially restored: %q", got)
	}
	if got := readWorkspaceFile(t, root, "b.txt"); got != "committed by user" {
		t.Fatalf("user commit was overwritten: %q", got)
	}
}

// TestUndoRestoresWhenFilesUnchangedAndRechecksEachWrite verifies the normal
// path still restores every manifest file and that the CAS recheck runs once
// per restored file.
func TestUndoRestoresWhenFilesUnchangedAndRechecksEachWrite(t *testing.T) {
	root := testWorkspace(t, map[string]string{"a.txt": "before a", "b.txt": "before b"})
	cp, err := CaptureCheckpoint(root, []string{"a.txt", "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "a.txt", "after a")
	writeWorkspaceFile(t, root, "b.txt", "after b")
	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatal(err)
	}

	var checked []string
	hook := func(target string) { checked = append(checked, filepath.Base(target)) }
	restored, err := undoCheckpoint(root, cp, hook)
	if err != nil {
		t.Fatalf("undoCheckpoint: %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("restored = %v, want two files", restored)
	}
	if len(checked) != 2 {
		t.Fatalf("CAS rechecks = %v, want one per restored file", checked)
	}
	if got := readWorkspaceFile(t, root, "a.txt"); got != "before a" {
		t.Fatalf("a.txt = %q", got)
	}
	if got := readWorkspaceFile(t, root, "b.txt"); got != "before b" {
		t.Fatalf("b.txt = %q", got)
	}
}

// TestUndoCASRefusesConcurrentChangeBetweenValidationAndWrite injects an edit
// in the window between validation and the first write, and asserts the CAS
// recheck aborts before clobbering concurrent work.
func TestUndoCASRefusesConcurrentChangeBetweenValidationAndWrite(t *testing.T) {
	root := testWorkspace(t, map[string]string{"a.txt": "before"})
	cp, err := CaptureCheckpoint(root, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, root, "a.txt", "after turn")
	if err := CompleteCheckpoint(root, &cp); err != nil {
		t.Fatal(err)
	}

	hook := func(target string) {
		if err := os.WriteFile(target, []byte("concurrent edit"), 0o644); err != nil {
			t.Fatalf("simulate concurrent edit: %v", err)
		}
	}
	restored, err := undoCheckpoint(root, cp, hook)
	if !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("error = %v, want ErrCheckpointConflict", err)
	}
	if len(restored) != 0 {
		t.Fatalf("restored = %v, want none (CAS must abort before writing)", restored)
	}
	if got := readWorkspaceFile(t, root, "a.txt"); got != "concurrent edit" {
		t.Fatalf("concurrent edit was clobbered: %q", got)
	}
}

// TestVerifyRestoreTargetRejectsDivergence covers every branch of the CAS
// helper: files the turn created, modified, or deleted, and a concurrent change
// to each of those states.
func TestVerifyRestoreTargetRejectsDivergence(t *testing.T) {
	root := testWorkspace(t, map[string]string{"exists.txt": "post", "gone.txt": "ignored"})
	path := filepath.Join(root, "exists.txt")
	missing := filepath.Join(root, "absent.txt")

	cases := []struct {
		name  string
		path  string
		file  CheckpointFile
		setup func(t *testing.T)
		want  bool
	}{
		{
			name: "modified file still at post state",
			path: path,
			file: CheckpointFile{Path: "exists.txt", SHA256: hashBytes([]byte("pre")), AfterSHA256: hashBytes([]byte("post"))},
		},
		{
			name: "modified file advanced",
			path: path,
			file: CheckpointFile{Path: "exists.txt", SHA256: hashBytes([]byte("pre")), AfterSHA256: hashBytes([]byte("post"))},
			setup: func(t *testing.T) {
				writeWorkspaceFile(t, root, "exists.txt", "newer commit")
			},
			want: true,
		},
		{
			name: "created file still present",
			path: path,
			file: CheckpointFile{Path: "exists.txt", Absent: true, AfterSHA256: hashBytes([]byte("post"))},
		},
		{
			name: "created file already removed",
			path: path,
			file: CheckpointFile{Path: "exists.txt", Absent: true, AfterSHA256: hashBytes([]byte("post"))},
			setup: func(t *testing.T) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
			want: true,
		},
		{
			name: "deleted file still absent",
			path: missing,
			file: CheckpointFile{Path: "absent.txt", SHA256: hashBytes([]byte("pre")), AfterAbsent: true},
		},
		{
			name: "deleted file recreated concurrently",
			path: missing,
			file: CheckpointFile{Path: "absent.txt", SHA256: hashBytes([]byte("pre")), AfterAbsent: true},
			setup: func(t *testing.T) {
				writeWorkspaceFile(t, root, "absent.txt", "recreated")
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeWorkspaceFile(t, root, "exists.txt", "post")
			_ = os.Remove(missing)
			if tc.setup != nil {
				tc.setup(t)
			}
			err := verifyRestoreTarget(tc.path, tc.file)
			if tc.want && !errors.Is(err, ErrCheckpointConflict) {
				t.Fatalf("verifyRestoreTarget error = %v, want ErrCheckpointConflict", err)
			}
			if !tc.want && err != nil {
				t.Fatalf("verifyRestoreTarget: %v", err)
			}
		})
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
