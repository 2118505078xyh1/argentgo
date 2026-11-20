package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const plantedSecret = "AKIAFAKESECRETKEY1234"

// exportDirSession writes records to <dir>/<id>.jsonl and returns both, so the
// tests exercise the directory-based ExportProject entry point.
func exportDirSession(t *testing.T, id string, records []Record) (string, string) {
	t.Helper()
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, id+fileExt), records)
	return dir, id
}

func exportFixture(t *testing.T) (string, string) {
	t.Helper()
	stamp := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	return exportDirSession(t, "s1", []Record{
		{ID: "n1", TS: stamp, Kind: KindHeader, Data: json.RawMessage(`{"title":"export demo"}`)},
		{ID: "n2", Parent: "n1", TS: stamp, Kind: KindUser, Data: json.RawMessage(`{"text":"hello world"}`)},
		{ID: "n3", Parent: "n2", TS: stamp, Kind: KindAssistant, Data: json.RawMessage(`{"text":"sure <script>alert(1)</script>"}`)},
		{ID: "n4", Parent: "n3", TS: stamp, Kind: KindTool, Data: json.RawMessage(`{"name":"fs.read","path":"/home/u/AKIAFAKESECRETKEY1234/secret.txt","command":"AKIAFAKESECRETKEY1234","output":"tool said AKIAFAKESECRETKEY1234"}`)},
	})
}

func TestExportProjectJSONStructure(t *testing.T) {
	dir, id := exportFixture(t)
	out, err := ExportProject(dir, id, "", ExportOptions{Format: "json"})
	if err != nil {
		t.Fatalf("ExportProject json: %v", err)
	}
	var entries []exportEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("unmarshal json export: %v\n%s", err, out)
	}
	wantRoles := []string{"system", "user", "assistant", "tool"}
	wantKinds := []string{KindHeader, KindUser, KindAssistant, KindTool}
	if len(entries) != len(wantRoles) {
		t.Fatalf("entries = %d, want %d: %s", len(entries), len(wantRoles), out)
	}
	for i, entry := range entries {
		if entry.Role != wantRoles[i] || entry.Kind != wantKinds[i] {
			t.Errorf("entry %d = role:%q kind:%q, want role:%q kind:%q", i, entry.Role, entry.Kind, wantRoles[i], wantKinds[i])
		}
		if entry.TS == "" {
			t.Errorf("entry %d has no timestamp", i)
		}
	}
	if entries[3].Tool != "fs.read" {
		t.Errorf("tool name = %q, want fs.read", entries[3].Tool)
	}
	if !strings.Contains(entries[1].Text, "hello world") {
		t.Errorf("user text = %q", entries[1].Text)
	}
}

func TestExportProjectMarkdownStructure(t *testing.T) {
	dir, id := exportFixture(t)
	out, err := ExportProject(dir, id, "", ExportOptions{Format: "markdown"})
	if err != nil {
		t.Fatalf("ExportProject markdown: %v", err)
	}
	text := string(out)
	for _, want := range []string{"# Session s1\n\n", "**user**: hello world", "**assistant**:", "**tool**:"} {
		if !strings.Contains(text, want) {
			t.Errorf("markdown missing %q: %s", want, text)
		}
	}
}

func TestExportProjectHTMLDelegatesToRenderHTML(t *testing.T) {
	dir, id := exportFixture(t)
	out, err := ExportProject(dir, id, "", ExportOptions{Format: "html"})
	if err != nil {
		t.Fatalf("ExportProject html: %v", err)
	}
	html := string(out)
	if html == "" || !strings.Contains(html, "<!doctype html>") {
		t.Fatalf("html not delegated: %q", html)
	}
	if !strings.Contains(html, "hello world") || !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("html lost escaped content: %s", html)
	}
	if strings.Contains(html, "<script") {
		t.Errorf("html contains raw script tag: %s", html)
	}
}

func TestSanitizeRecordRedactsSensitiveFields(t *testing.T) {
	record := Record{ID: "n9", Kind: KindTool, Data: json.RawMessage(`{
		"name":"fs.read",
		"path":"/home/u/AKIAFAKESECRETKEY1234/secret.txt",
		"command":"AKIAFAKESECRETKEY1234",
		"args":["AKIAFAKESECRETKEY1234","second"],
		"output":"tool said AKIAFAKESECRETKEY1234",
		"title":"secret title",
		"url":"https://u:AKIAFAKESECRETKEY1234@example.com",
		"note":"plain note"
	}`)}
	got := sanitizeRecord(record)
	var data map[string]any
	if err := json.Unmarshal(got.Data, &data); err != nil {
		t.Fatalf("unmarshal sanitized data: %v", err)
	}
	for key, kind := range map[string]string{
		"path": "path", "command": "command", "output": "tool-output",
		"title": "title", "url": "url", "note": "text",
	} {
		want := redactedText(kind, "n9")
		if data[key] != want {
			t.Errorf("field %q = %v, want %q", key, data[key], want)
		}
	}
	args, ok := data["args"].([]any)
	if !ok || len(args) != 2 || args[0] != redactedText("tool-args", "n9") || args[1] != redactedText("tool-args", "n9") {
		t.Errorf("args = %#v, want both redacted", data["args"])
	}
	for _, key := range []string{"path", "command", "output", "title", "url", "note", "args"} {
		if strings.Contains(string(got.Data), plantedSecret) {
			t.Fatalf("secret survived in field %q: %s", key, got.Data)
		}
	}
}

func TestExportProjectSanitizeRemovesSecretFromAllFormats(t *testing.T) {
	dir, id := exportFixture(t)
	for _, format := range []string{"html", "json", "markdown"} {
		t.Run(format, func(t *testing.T) {
			out, err := ExportProject(dir, id, "", ExportOptions{Format: format, Sanitize: true})
			if err != nil {
				t.Fatalf("sanitized %s export: %v", format, err)
			}
			if strings.Contains(string(out), plantedSecret) {
				t.Fatalf("%s export leaked the planted secret: %s", format, out)
			}
			if strings.Contains(string(out), "hello world") {
				t.Errorf("%s export kept raw text: %s", format, out)
			}
			if !strings.Contains(string(out), "[redacted:") {
				t.Errorf("%s export has no redaction markers: %s", format, out)
			}
		})
	}
}

func TestExportProjectWithoutSanitizeKeepsContent(t *testing.T) {
	dir, id := exportFixture(t)
	for _, format := range []string{"html", "json", "markdown"} {
		t.Run(format, func(t *testing.T) {
			out, err := ExportProject(dir, id, "", ExportOptions{Format: format})
			if err != nil {
				t.Fatalf("%s export: %v", format, err)
			}
			if !strings.Contains(string(out), "hello world") {
				t.Errorf("%s export dropped raw content: %s", format, out)
			}
			if !strings.Contains(string(out), plantedSecret) {
				t.Errorf("%s export dropped raw tool payload: %s", format, out)
			}
		})
	}
}

func TestExportProjectSelectsBranch(t *testing.T) {
	stamp := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	dir, id := exportDirSession(t, "fork", []Record{
		{ID: "n1", TS: stamp, Kind: KindHeader, Data: json.RawMessage(`{"title":"fork"}`)},
		{ID: "n2", Parent: "n1", TS: stamp, Kind: KindUser, Data: json.RawMessage(`"root"`)},
		{ID: "n3", Parent: "n2", TS: stamp, Kind: KindAssistant, Data: json.RawMessage(`"branch A"`)},
		{ID: "n4", Parent: "n2", TS: stamp, Kind: KindAssistant, Data: json.RawMessage(`"branch B"`)},
	})
	for _, tc := range []struct {
		branch string
		want   string
		absent string
	}{
		{branch: "n3", want: "branch A", absent: "branch B"},
		{branch: "n4", want: "branch B", absent: "branch A"},
		{branch: "", want: "branch B", absent: "branch A"}, // active leaf
	} {
		out, err := ExportProject(dir, id, tc.branch, ExportOptions{Format: "json"})
		if err != nil {
			t.Fatalf("branch %q: %v", tc.branch, err)
		}
		text := string(out)
		if !strings.Contains(text, tc.want) || strings.Contains(text, tc.absent) {
			t.Errorf("branch %q produced wrong path: %s", tc.branch, text)
		}
	}
}

func TestExportProjectRejectsUnknownBranchAndFormat(t *testing.T) {
	dir, id := exportFixture(t)
	if _, err := ExportProject(dir, id, "nope", ExportOptions{Format: "json"}); err == nil {
		t.Fatal("unknown branch succeeded")
	}
	if _, err := ExportProject(dir, id, "", ExportOptions{Format: "yaml"}); err == nil {
		t.Fatal("unsupported format succeeded")
	}
}

func TestExportProjectCapsOutput(t *testing.T) {
	huge := strings.Repeat("x", maxExportBytes+1024)
	dir, id := exportDirSession(t, "huge", []Record{
		{ID: "n1", Kind: KindHeader, Data: json.RawMessage(`{"title":"huge"}`)},
		{ID: "n2", Parent: "n1", Kind: KindUser, Data: mustJSON(t, map[string]any{"text": huge})},
	})
	for _, format := range []string{"json", "markdown", "html"} {
		out, err := ExportProject(dir, id, "", ExportOptions{Format: format})
		if err != nil {
			t.Fatalf("%s export: %v", format, err)
		}
		if len(out) > maxExportBytes {
			t.Errorf("%s export = %d bytes, over cap %d", format, len(out), maxExportBytes)
		}
		if !strings.Contains(string(out), "[truncated:") {
			t.Errorf("%s export missing truncation marker", format)
		}
	}
}

func TestExportProjectEmptySession(t *testing.T) {
	dir := t.TempDir()
	writeRawSession(t, filepath.Join(dir, "empty"+fileExt), nil)
	for _, format := range []string{"json", "markdown", "html"} {
		out, err := ExportProject(dir, "empty", "", ExportOptions{Format: format, Sanitize: true})
		if err != nil {
			t.Fatalf("empty %s export: %v", format, err)
		}
		if len(out) == 0 {
			t.Errorf("empty %s export produced no bytes", format)
		}
	}
}

func TestExportProjectAcceptsSessionPointer(t *testing.T) {
	dir, id := exportFixture(t)
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	sess, err := store.OpenSession(id)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	out, err := ExportProject(sess, id, "", ExportOptions{Format: "json"})
	if err != nil {
		t.Fatalf("ExportProject(*Session): %v", err)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Errorf("session-pointer export lost content: %s", out)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
