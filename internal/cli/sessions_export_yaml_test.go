package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// yamlFixture writes a small session with CJK content, used by the YAML export
// tests. The helper names match the other sessions tests in this package.
func yamlFixture(t *testing.T) string {
	t.Helper()
	storage := t.TempDir()
	writeJSONLSession(t, storage, "cjk", []map[string]any{
		record("n1", "", "2026-01-01T00:00:00Z", "header", map[string]any{"title": "中文标题"}),
		record("n2", "n1", "2026-01-01T00:00:01Z", "user", map[string]any{"text": "你好，世界 — CJK 内容"}),
		record("n3", "n2", "2026-01-01T00:00:02Z", "assistant", map[string]any{"text": "回答：完成"}),
	})
	return storage
}

type yamlEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Kind string `json:"kind"`
}

// decodeYAMLExport asserts the exported file is a YAML comment followed by a
// JSON body (JSON being a YAML 1.2 subset) and returns the decoded entries.
func decodeYAMLExport(t *testing.T, data []byte) []yamlEntry {
	t.Helper()
	if !bytes.HasPrefix(data, []byte(yamlExportHeader)) {
		t.Fatalf("missing YAML marker: %q", data)
	}
	body := bytes.TrimPrefix(data, []byte(yamlExportHeader))
	if !json.Valid(body) {
		t.Fatalf("YAML body is not valid JSON (hence not valid YAML): %q", body)
	}
	var entries []yamlEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode YAML body: %v", err)
	}
	return entries
}

func TestSessionsExportYAMLFormatAcceptedAndValid(t *testing.T) {
	storage := yamlFixture(t)
	target := filepath.Join(t.TempDir(), "transcript.yaml")
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "cjk", "--format", "yaml", "-o", target,
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "wrote "+target) {
		t.Fatalf("out = %q", out.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	entries := decodeYAMLExport(t, data)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3: %+v", len(entries), entries)
	}
	if entries[1].Role != "user" || !strings.Contains(entries[1].Text, "你好，世界") {
		t.Errorf("user entry = %+v", entries[1])
	}
	if entries[2].Role != "assistant" || entries[2].Text != "回答：完成" {
		t.Errorf("assistant entry = %+v", entries[2])
	}
	// CJK must survive as raw UTF-8, not \uXXXX escapes.
	if bytes.Contains(data, []byte(`\u4f60`)) || !bytes.Contains(data, []byte("你好，世界")) {
		t.Errorf("CJK not preserved as UTF-8: %q", data)
	}
}

func TestSessionsExportYAMLEqualsFormToStdout(t *testing.T) {
	storage := yamlFixture(t)
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "cjk", "--format=yaml",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	entries := decodeYAMLExport(t, out.Bytes())
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
}

func TestSessionsExportDefaultFormatIsStillHTML(t *testing.T) {
	storage := yamlFixture(t)
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "cjk",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "<!doctype html>") {
		t.Fatalf("default export is not HTML: %q", out.String())
	}
	if strings.Contains(out.String(), yamlExportHeader) {
		t.Fatalf("default export carries YAML marker: %q", out.String())
	}
}

func TestSessionsExportYAMLWithSanitizeRedacts(t *testing.T) {
	storage := t.TempDir()
	secret := "token = 2118505078xyh1"
	writeJSONLSession(t, storage, "secret", []map[string]any{
		record("n1", "", "2026-01-01T00:00:00Z", "header", map[string]any{"title": "demo"}),
		record("n2", "n1", "2026-01-01T00:00:01Z", "tool", map[string]any{"output": secret}),
	})
	stdio, out, errOut := newTestIO("", nil)
	code := RunWith(App{IO: stdio, StorageDir: storage}, []string{
		"sessions", "export", "secret", "--format", "yaml", "--sanitize",
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
	entries := decodeYAMLExport(t, out.Bytes())
	var combined strings.Builder
	for _, entry := range entries {
		combined.WriteString(entry.Text)
	}
	if strings.Contains(combined.String(), secret) || !strings.Contains(combined.String(), "[redacted:") {
		t.Fatalf("sanitize failed: %q", combined.String())
	}
}

func TestIsYAMLExport(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   bool
	}{
		{"yaml", true},
		{"YAML", true},
		{" yaml ", true},
		{"yml", false},
		{"json", false},
		{"html", false},
		{"", false},
	} {
		if got := isYAMLExport(tc.format); got != tc.want {
			t.Errorf("isYAMLExport(%q) = %v, want %v", tc.format, got, tc.want)
		}
	}
}

func TestJSONExportToYAMLRejectsInvalidJSON(t *testing.T) {
	if _, err := jsonExportToYAML([]byte("not json")); err == nil {
		t.Fatal("invalid JSON accepted as YAML")
	}
}
