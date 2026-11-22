package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

func TestParseSessionCheckpointFlags(t *testing.T) {
	opts, _, err := parseArgs([]string{
		"--undo", "--retry", "--title", "my title", "--session", "abc",
	}, func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Undo || !opts.Retry || opts.Title != "my title" || opts.Session != "abc" {
		t.Fatalf("options = %+v", opts)
	}
}

func TestSessionsUndoRetryNameAndAttemptGrep(t *testing.T) {
	storage := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, "first prompt"); err != nil {
		t.Fatal(err)
	}
	cp, err := session.CaptureCheckpoint(workspace, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a.txt"), []byte("turn edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := session.CompleteCheckpoint(workspace, &cp); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.RecordCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindAssistant, "answer"); err != nil {
		t.Fatal(err)
	}
	stdio, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "undo", sess.ID(), "-C", workspace}); code != 0 {
		t.Fatalf("undo exit = %d, err %q", code, errOut.String())
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "a.txt")); err != nil || string(data) != "before" {
		t.Fatalf("undo file = %q, %v", data, err)
	}
	if !strings.Contains(out.String(), "restored 1 file") {
		t.Fatalf("undo output = %q", out.String())
	}

	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "name", sess.ID(), "renamed session"}); code != 0 {
		t.Fatalf("name exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "renamed") {
		t.Fatalf("name output = %q", out.String())
	}

	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "retry", sess.ID()}); code != 0 {
		t.Fatalf("retry exit = %d, err %q", code, errOut.String())
	}
	forkID := strings.TrimSpace(out.String())
	if forkID == "" || forkID == sess.ID() {
		t.Fatalf("retry output = %q", out.String())
	}

	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "tree", forkID}); code != 0 {
		t.Fatalf("tree exit = %d, err %q", code, errOut.String())
	}
	// tree 现在渲染 ASCII id/parent 树（不再输出 link/retried 文本），
	// 只需确认标题指向 fork 会话且活动叶被标记。
	if !strings.Contains(out.String(), "会话树: "+forkID) || !strings.Contains(out.String(), "← active") {
		t.Fatalf("tree output = %q", out.String())
	}

	stdio, out, errOut = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "--grep", "forked from checkpoint"}); code != 0 {
		t.Fatalf("grep exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), forkID) {
		t.Fatalf("grep did not find attempt record: %q", out.String())
	}
}

func TestUndoFlagRestoresWithoutRunner(t *testing.T) {
	storage := t.TempDir()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "a.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := session.CaptureCheckpoint(workspace, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := session.CompleteCheckpoint(workspace, &cp); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.RecordCheckpoint(cp); err != nil {
		t.Fatal(err)
	}

	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"--session", sess.ID(), "--undo", "-C", workspace, "prompt is ignored",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "restored 1 file") {
		t.Fatalf("output = %q", out.String())
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "before" {
		t.Fatalf("restored file = %q, %v", data, err)
	}
}

func TestApplyTitleToSession(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("")
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyTitleToSession(sess, "explicit"); err != nil || sess.Title() != "explicit" {
		t.Fatalf("title = %q, %v", sess.Title(), err)
	}
}

func TestFSStoreTitleRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	line, err := json.Marshal(map[string]any{
		"id": "n1", "kind": "user", "data": map[string]any{"text": "token = 2118505078xyh1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	infos, err := (&fsStore{dir: dir}).List()
	if err != nil || len(infos) != 1 {
		t.Fatalf("infos = %+v, %v", infos, err)
	}
	if strings.Contains(infos[0].Title, "2118505078xyh1") || !strings.Contains(infos[0].Title, "redacted") {
		t.Fatalf("title = %q", infos[0].Title)
	}
}
