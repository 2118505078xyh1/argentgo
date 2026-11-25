package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/session"
)

const claudeImportTranscript = `{"type":"summary","summary":"demo"}
{"type":"user","uuid":"u1","timestamp":"2026-01-02T03:04:05Z","message":{"role":"user","content":"你好，世界"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-01-02T03:04:06Z","message":{"role":"assistant","content":[{"type":"text","text":"Sure"},{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"a.txt"}}],"usage":{"input_tokens":10,"output_tokens":5}}}
{"type":"user","uuid":"u2","timestamp":"2026-01-02T03:04:07Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"file contents 中文"}]}}
`

const codexImportTranscript = `{"type":"session_meta","payload":{"id":"s1","cwd":"/tmp"}}
{"type":"response_item","timestamp":"2026-01-02T03:04:05Z","payload":{"type":"message","role":"user","id":"m1","content":[{"type":"input_text","text":"请修复 bug"}]}}
{"type":"response_item","timestamp":"2026-01-02T03:04:06Z","payload":{"type":"function_call","id":"f1","call_id":"call_1","name":"patch","arguments":"{\"file\":\"a.go\"}"}}
{"type":"response_item","timestamp":"2026-01-02T03:04:07Z","payload":{"type":"function_call_output","id":"o1","call_id":"call_1","output":"done ✅"}}
{"type":"response_item","timestamp":"2026-01-02T03:04:08Z","payload":{"type":"message","role":"assistant","id":"m2","content":[{"type":"output_text","text":"已修复"}]}}
`

func writeImportTranscript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runSessionsImport(t *testing.T, storage, path string, extra ...string) (string, string, int) {
	t.Helper()
	stdio, out, errOut := newTestIO("", nil)
	args := append([]string{"sessions", "import", path}, extra...)
	code := RunWith(App{IO: stdio, StorageDir: storage}, args)
	return strings.TrimSpace(out.String()), errOut.String(), code
}

func readImportedMessages(t *testing.T, storage, id string) []session.Entry {
	t.Helper()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.OpenSession(id)
	if err != nil {
		t.Fatalf("reopen imported session %s: %v", id, err)
	}
	return sess.Messages()
}

func assertImportedMessages(t *testing.T, msgs []session.Entry, wantRoles, wantContents []string) {
	t.Helper()
	if len(msgs) != len(wantRoles) {
		t.Fatalf("messages = %+v, want %d entries", msgs, len(wantRoles))
	}
	for i, msg := range msgs {
		if msg.Role != wantRoles[i] {
			t.Errorf("message %d role = %q, want %q", i, msg.Role, wantRoles[i])
		}
		if msg.Content != wantContents[i] {
			t.Errorf("message %d content = %q, want %q", i, msg.Content, wantContents[i])
		}
	}
}

func TestSessionsImportClaudeCodePersistsConversation(t *testing.T) {
	storage := t.TempDir()
	path := writeImportTranscript(t, claudeImportTranscript)
	id, errOut, code := runSessionsImport(t, storage, path, "--format", "claude")
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut)
	}
	if id == "" || strings.ContainsAny(id, `/\`) {
		t.Fatalf("session id = %q", id)
	}

	msgs := readImportedMessages(t, storage, id)
	assertImportedMessages(t, msgs,
		[]string{"user", "assistant", "tool"},
		[]string{"你好，世界", "Sure", "file contents 中文"})
	if len(msgs[1].Data) == 0 || !strings.Contains(string(msgs[1].Data), `"call_1"`) {
		t.Errorf("assistant tool_calls not preserved: %s", msgs[1].Data)
	}
	if !strings.Contains(string(msgs[1].Data), `"input_tokens":10`) {
		t.Errorf("usage not preserved: %s", msgs[1].Data)
	}
	if msgs[2].ToolCallID != "call_1" {
		t.Errorf("tool call id = %q, want call_1", msgs[2].ToolCallID)
	}

	// The printed id must be usable by the other sessions subcommands.
	stdio, out, errBuf := newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "show", id}); code != 0 {
		t.Fatalf("show exit = %d, err %q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "你好，世界") {
		t.Errorf("show did not surface imported CJK text: %q", out.String())
	}
	stdio, out, _ = newTestIO("", nil)
	if code := RunWith(App{IO: stdio, StorageDir: storage}, []string{"sessions", "list"}); code != 0 {
		t.Fatalf("list exit = %d", code)
	}
	if !strings.Contains(out.String(), id) {
		t.Errorf("list did not include imported session: %q", out.String())
	}
}

func TestSessionsImportCodexPersistsConversation(t *testing.T) {
	storage := t.TempDir()
	path := writeImportTranscript(t, codexImportTranscript)
	id, errOut, code := runSessionsImport(t, storage, path, "--format=codex")
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut)
	}

	msgs := readImportedMessages(t, storage, id)
	assertImportedMessages(t, msgs,
		[]string{"user", "assistant", "tool", "assistant"},
		[]string{"请修复 bug", "", "done ✅", "已修复"})
	if !strings.Contains(string(msgs[1].Data), `"call_1"`) {
		t.Errorf("codex function_call not preserved: %s", msgs[1].Data)
	}
	if msgs[1].Name != "patch" {
		t.Errorf("tool name = %q, want patch", msgs[1].Name)
	}
	if msgs[2].ToolCallID != "call_1" {
		t.Errorf("tool call id = %q, want call_1", msgs[2].ToolCallID)
	}
}

func TestSessionsImportAutoSniffsFormat(t *testing.T) {
	cases := []struct {
		name       string
		transcript string
		wantFirst  string
	}{
		{"claude after summary line", claudeImportTranscript, "你好，世界"},
		{"codex from session_meta", codexImportTranscript, "请修复 bug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			path := writeImportTranscript(t, tc.transcript)
			id, errOut, code := runSessionsImport(t, storage, path)
			if code != 0 {
				t.Fatalf("exit = %d, err %q", code, errOut)
			}
			msgs := readImportedMessages(t, storage, id)
			if len(msgs) == 0 || msgs[0].Content != tc.wantFirst {
				t.Fatalf("first message = %+v, want %q", msgs, tc.wantFirst)
			}
		})
	}
}

func TestSessionsImportRejectsInvalidAndEmptyFiles(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"not json", "this is not json\n", "unrecognized transcript format"},
		{"unknown event type", `{"type":"progress"}` + "\n", "unrecognized transcript format"},
		{"empty file", "", "empty transcript"},
		{"blank lines only", "\n  \n", "empty transcript"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage := t.TempDir()
			path := writeImportTranscript(t, tc.body)
			id, errOut, code := runSessionsImport(t, storage, path)
			if code != 1 {
				t.Fatalf("exit = %d, want 1 (out %q)", code, id)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Errorf("err = %q, want substring %q", errOut, tc.want)
			}
		})
	}
}

func TestSessionsImportRejectsMissingFile(t *testing.T) {
	storage := t.TempDir()
	path := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	_, errOut, code := runSessionsImport(t, storage, path)
	if code != 1 || !strings.Contains(errOut, "read ") {
		t.Fatalf("exit = %d, err %q", code, errOut)
	}
}

func TestSessionsImportRejectsBadArguments(t *testing.T) {
	storage := t.TempDir()
	path := writeImportTranscript(t, claudeImportTranscript)
	cases := []struct {
		name string
		args []string
	}{
		{"unknown format", []string{"--format", "bogus"}},
		{"missing format value", []string{"--format"}},
		{"unexpected argument", []string{"--wat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errOut, code := runSessionsImport(t, storage, path, tc.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (err %q)", code, errOut)
			}
		})
	}
}

func TestSessionsImportFormatFlagIsHonored(t *testing.T) {
	storage := t.TempDir()
	cases := []struct {
		name       string
		transcript string
		format     string
	}{
		{"claude file forced to codex", claudeImportTranscript, "--format=codex"},
		{"codex file forced to claude", codexImportTranscript, "--format=claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeImportTranscript(t, tc.transcript)
			_, errOut, code := runSessionsImport(t, storage, path, tc.format)
			if code != 1 || !strings.Contains(errOut, "no messages found") {
				t.Fatalf("exit = %d, err %q", code, errOut)
			}
		})
	}
}

func TestImportEntryKindMapsRoles(t *testing.T) {
	cases := []struct {
		role string
		want string
		ok   bool
	}{
		{"user", session.KindUser, true},
		{"assistant", session.KindAssistant, true},
		{"tool", session.KindTool, true},
		{"system", session.KindSummary, true},
		{" User ", session.KindUser, true},
		{"progress", "", false},
	}
	for _, tc := range cases {
		got, ok := importEntryKind(tc.role)
		if got != tc.want || ok != tc.ok {
			t.Errorf("importEntryKind(%q) = (%q, %v), want (%q, %v)", tc.role, got, ok, tc.want, tc.ok)
		}
	}
}

func TestImportEntryPayloadSynthesizesMissingData(t *testing.T) {
	// ReconcileToolPairs inserts aborted tool results with no Data; the payload
	// must still carry the content and tool-call id for read-back.
	entry := session.Entry{Role: "tool", Content: "[aborted] tool did not run", ToolCallID: "call_9"}
	var payload map[string]any
	if err := json.Unmarshal(importEntryPayload(entry), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["role"] != "tool" || payload["content"] != entry.Content || payload["tool_call_id"] != "call_9" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestImportEntryPayloadPreservesRawData(t *testing.T) {
	// 合法的 Data 会原样透传，这样当记录再次被序列化时，超出 float64 精确范围的
	// 数值型 usage 字段不会发生变化。
	raw := json.RawMessage(`{"role":"assistant","content":"x","tool_calls":[{"id":"call_1"}],"usage":{"input_tokens":9007199254740993}}`)
	got := importEntryPayload(session.Entry{Role: "assistant", Content: "x", Data: raw})
	if string(got) != string(raw) {
		t.Fatalf("payload = %s, want %s", got, raw)
	}
}
