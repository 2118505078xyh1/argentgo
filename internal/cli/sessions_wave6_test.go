package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argent-go/internal/session"
)

func writeJSONLSession(t *testing.T, storage, id string, records []map[string]any) {
	t.Helper()
	dir := filepath.Join(storage, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func record(id, parent, ts, kind string, data any) map[string]any {
	return map[string]any{"id": id, "parent": parent, "ts": ts, "kind": kind, "data": data}
}

func TestSessionsGrepReadsStorageFilesWithLimit(t *testing.T) {
	storage := t.TempDir()
	writeJSONLSession(t, storage, "newer", []map[string]any{
		record("n1", "", "2026-01-02T10:00:00Z", "header", map[string]any{"title": "new title"}),
		record("n2", "n1", "2026-01-02T10:00:01Z", "tool", map[string]any{"output": "needle in tool output"}),
	})
	writeJSONLSession(t, storage, "older", []map[string]any{
		record("n1", "", "2026-01-01T10:00:00Z", "header", map[string]any{"title": "old title"}),
		record("n2", "n1", "2026-01-01T10:00:01Z", "tool", map[string]any{"output": "needle in old output"}),
	})

	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "--grep", "NEEDLE", "--limit", "1"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "newer:2\t") || !strings.Contains(lines[0], "tool output") {
		t.Fatalf("out = %q", out.String())
	}

	stdio, out, _ = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "--grep", "absent"}); code != 0 {
		t.Fatalf("no-match exit = %d", code)
	}
	if !strings.Contains(out.String(), "no matches") {
		t.Fatalf("no-match out = %q", out.String())
	}
}

func TestSessionsGrepRedactsSecretInSnippet(t *testing.T) {
	storage := t.TempDir()
	writeJSONLSession(t, storage, "secret", []map[string]any{
		record("n1", "", "2026-01-02T10:00:00Z", "tool",
			map[string]any{"output": "token = 2118505078xyh1 done"}),
	})
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "--grep", "token"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if strings.Contains(out.String(), "2118505078xyh1") || !strings.Contains(out.String(), "[redacted:") {
		t.Fatalf("snippet = %q", out.String())
	}
}

type fakeGrepStore struct {
	*fakeStore
	query string
	limit int
}

func (f *fakeGrepStore) Search(query string, limit int) ([]session.Hit, error) {
	f.query, f.limit = query, limit
	return []session.Hit{{
		Path: "injected.jsonl", ID: "injected", Line: 7,
		Snippet: "injected hit", TS: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
	}}, nil
}

func TestSessionsGrepPrefersInjectedStore(t *testing.T) {
	store := &fakeGrepStore{fakeStore: &fakeStore{}}
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, Sessions: store}, []string{"sessions", "--grep", "hello", "--limit", "3"})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if store.query != "hello" || store.limit != 3 {
		t.Fatalf("injected Search called with %q/%d", store.query, store.limit)
	}
	if !strings.Contains(out.String(), "injected.jsonl") || !strings.Contains(out.String(), "injected hit") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestSessionsTreeAndExportForkedSession(t *testing.T) {
	storage := t.TempDir()
	stamp := "2026-11-19T12:00:00Z"
	writeJSONLSession(t, storage, "fork", []map[string]any{
		record("n1", "", stamp, "header", map[string]any{"title": "fork demo"}),
		record("n2", "n1", stamp, "user", map[string]any{"text": "root question"}),
		record("n3", "n2", stamp, "assistant", map[string]any{"text": "branch A"}),
		record("n4", "n2", stamp, "assistant", map[string]any{"text": "branch B <script>alert(1)</script>"}),
	})
	app := App{StorageDir: storage}

	stdio, out, errOut := newTestIO("", nil)
	app.IO = stdio
	if code := RunWith(app, []string{"sessions", "tree", "fork"}); code != 0 {
		t.Fatalf("tree exit = %d, err %q", code, errOut.String())
	}
	for _, want := range []string{"n1", "n2", "n3", "n4", "parent=n2", "depth=2", "*n4"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("tree missing %q: %s", want, out.String())
		}
	}

	target := filepath.Join(t.TempDir(), "transcript.html")
	stdio, out, errOut = newTestIO("", nil)
	app.IO = stdio
	if code := RunWith(app, []string{"sessions", "export", "fork", "-o", target}); code != 0 {
		t.Fatalf("export exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "wrote "+target) {
		t.Fatalf("export out = %q", out.String())
	}
	html, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "&lt;script&gt;") || strings.Contains(string(html), "<script") {
		t.Fatalf("HTML escaping failed: %s", html)
	}
	for _, forbidden := range []string{"http://", "https://", "src=", "cdn."} {
		if strings.Contains(strings.ToLower(string(html)), forbidden) {
			t.Errorf("HTML has network reference %q", forbidden)
		}
	}

	stdio, _, errOut = newTestIO("", nil)
	app.IO = stdio
	if code := RunWith(app, []string{"sessions", "tree", "missing"}); code != 1 {
		t.Fatalf("missing tree exit = %d, want 1", code)
	}
	stdio, _, errOut = newTestIO("", nil)
	app.IO = stdio
	if code := RunWith(app, []string{"sessions", "export", "missing"}); code != 1 || !strings.Contains(errOut.String(), "no session") {
		t.Fatalf("missing export exit = %d, err %q", code, errOut.String())
	}
}
