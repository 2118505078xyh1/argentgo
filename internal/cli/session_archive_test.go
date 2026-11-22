package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// newArchiveApp 构造一个带临时存储目录的 App，归档与会话文件都落在其中。
func newArchiveApp(t *testing.T) (App, string) {
	t.Helper()
	storage := t.TempDir()
	sessionsDir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := App{
		StorageDir: storage,
		IO: IO{
			Out: &out, Err: &errOut,
			Env: func(string) string { return "" },
		},
	}
	return app, sessionsDir
}

func writeArchiveSession(t *testing.T, dir, id string, entries []session.Entry) {
	t.Helper()
	ref, err := session.WriteCompactionArchiveRef(dir, "arch-"+id, entries)
	if err != nil {
		t.Fatalf("WriteCompactionArchiveRef: %v", err)
	}
	comp, _ := json.Marshal(session.Compaction{Summary: "摘要", Archive: ref})
	rec, _ := json.Marshal(session.Record{ID: "c1", Kind: session.KindCompaction, Data: comp})
	content := string(rec) + "\n"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsArchiveReadsBack(t *testing.T) {
	app, dir := newArchiveApp(t)
	entries := []session.Entry{
		{Role: "user", Content: "原始问题"},
		{Role: "assistant", Content: "被压缩掉的原始回答\n第二行"},
	}
	writeArchiveSession(t, dir, "sess1", entries)

	code := sessionsArchive(app, "sess1", nil)
	if code != 0 {
		t.Fatalf("code = %d, err = %s", code, app.IO.Err.(*bytes.Buffer).String())
	}
	out := app.IO.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "被压缩掉的原始回答") {
		t.Fatalf("archive text missing original content: %s", out)
	}
	// 多行内容折叠为单行。
	if strings.Contains(out, "\n第二行") {
		t.Fatalf("text should be single-line per entry: %s", out)
	}
}

func TestSessionsArchiveJSONFormat(t *testing.T) {
	app, dir := newArchiveApp(t)
	writeArchiveSession(t, dir, "sess2", []session.Entry{{Role: "user", Content: "x"}})

	code := sessionsArchive(app, "sess2", []string{"--format", "json"})
	if code != 0 {
		t.Fatalf("code = %d, err = %s", code, app.IO.Err.(*bytes.Buffer).String())
	}
	out := app.IO.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, `"Role"`) || !strings.Contains(out, "x") {
		t.Fatalf("json output wrong: %s", out)
	}
}

func TestSessionsArchiveNoArchive(t *testing.T) {
	app, dir := newArchiveApp(t)
	// 会话存在但没有压缩记录。
	if err := os.WriteFile(filepath.Join(dir, "empty.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := sessionsArchive(app, "empty", nil)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(app.IO.Out.(*bytes.Buffer).String(), "尚无压缩归档") {
		t.Fatalf("expected no-archive message, got %s", app.IO.Out.(*bytes.Buffer).String())
	}
}
