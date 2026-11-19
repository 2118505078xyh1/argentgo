package security

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRedactText(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{"api key", "calling with sk-test-abcdef0123456789 now", []string{"sk-test-abcdef0123456789"}, []string{"[redacted:api key]"}},
		{"bearer header", "Authorization: Bearer abc.def.ghi", []string{"abc.def.ghi"}, []string{"Bearer"}},
		{"lowercase bearer", "authorization=Bearer xyz123", []string{"xyz123"}, []string{"redacted"}},
		{"key value", "api_key=supersecretvalue", []string{"supersecretvalue"}, []string{"api_key"}},
		{"password pair", "password: hunter2", []string{"hunter2"}, []string{"password"}},
		{"github token", "token ghp_abcdef123456 end", []string{"ghp_abcdef123456"}, []string{"redacted"}},
		{"multiple", "sk-aaa111 and hf_bbb222", []string{"sk-aaa111", "hf_bbb222"}, []string{"redacted"}},
	}
	for _, tc := range cases {
		out := Redact(tc.in)
		for _, secret := range tc.absent {
			if strings.Contains(out, secret) {
				t.Fatalf("%s: %q leaked %q", tc.name, out, secret)
			}
		}
		for _, marker := range tc.present {
			if !strings.Contains(out, marker) {
				t.Fatalf("%s: %q lost %q", tc.name, out, marker)
			}
		}
	}
	if got := Redact("read the file src/main.rs and count the lines"); got != "read the file src/main.rs and count the lines" {
		t.Fatalf("ordinary text changed: %q", got)
	}
	if got := Redact("Bearer "); got != "Bearer " {
		t.Fatalf("a bare prefix is not a secret: %q", got)
	}
}

func TestRedactJSON(t *testing.T) {
	input := `{"path":"a.txt","password":"hunter2","config":{"api-key":"one","token":"plain-token"},` +
		`"steps":[{"tool":"read","token":"second-token"}],"port":5432,"note":"use sk-testvalue123"}`
	var value map[string]any
	if err := json.Unmarshal([]byte(RedactJSON([]byte(input))), &value); err != nil {
		t.Fatalf("redacted output is not JSON: %v", err)
	}
	if value["path"] != "a.txt" {
		t.Fatalf("path changed: %v", value["path"])
	}
	if value["port"] != float64(5432) {
		t.Fatalf("port changed: %v", value["port"])
	}
	if value["password"] != "[redacted:password]" {
		t.Fatalf("password: %v", value["password"])
	}
	config := value["config"].(map[string]any)
	if config["api-key"] != "[redacted:api key]" || config["token"] != "[redacted:token]" {
		t.Fatalf("nested secrets: %v", config)
	}
	steps := value["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("array shape changed: %v", steps)
	}
	step := steps[0].(map[string]any)
	if step["tool"] != "read" || step["token"] != "[redacted:token]" {
		t.Fatalf("array entry: %v", step)
	}
	if note, _ := value["note"].(string); strings.Contains(note, "sk-testvalue123") {
		t.Fatalf("free-form key leaked a key: %q", note)
	}
	if got := RedactJSON([]byte(`no json here sk-abcdef123456`)); strings.Contains(got, "sk-abcdef123456") {
		t.Fatalf("non-JSON fallback leaked: %q", got)
	}
}

func TestAuditLogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	log.now = func() time.Time { return fixed }

	err := log.Log("s1", "bash", Deny, "policy", json.RawMessage(`{"command":"ls","token":"abc123"}`))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	wantPath := filepath.Join(dir, "2026-01-02.jsonl")
	if log.Path() != wantPath {
		t.Fatalf("path = %q, want %q", log.Path(), wantPath)
	}
	entries, err := ReadAudit(wantPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Tool != "bash" || entry.Decision != "deny" || entry.Session != "s1" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.TS != "2026-01-02T03:04:05Z" {
		t.Fatalf("ts = %q", entry.TS)
	}
	if strings.Contains(entry.Args, "abc123") {
		t.Fatalf("args leaked a token: %q", entry.Args)
	}
}

func TestAuditRecordRedactsReasonAndArgs(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	err := log.Record(AuditEntry{
		Tool:     "bash",
		Decision: "deny",
		Reason:   "blocked because sk-abcdef123456 was in the command",
		Args:     `{"password":"hunter2"}`,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	entries, err := ReadAudit(log.Path())
	if err != nil || len(entries) != 1 {
		t.Fatalf("read: %v entries=%d", err, len(entries))
	}
	if strings.Contains(entries[0].Reason, "sk-abcdef123456") {
		t.Fatalf("reason leaked: %q", entries[0].Reason)
	}
	if strings.Contains(entries[0].Args, "hunter2") {
		t.Fatalf("args leaked: %q", entries[0].Args)
	}
	if entries[0].TS == "" {
		t.Fatal("Record must fill in a timestamp")
	}
}

func TestAuditLogIsThreadSafe(t *testing.T) {
	dir := t.TempDir()
	log := NewAuditLog(dir)
	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if err := log.Log("s", "read", Allow, "", json.RawMessage(`{"path":"a.txt"}`)); err != nil {
					t.Errorf("writer %d: %v", id, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	entries, err := ReadAudit(log.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != writers*perWriter {
		t.Fatalf("got %d entries, want %d", len(entries), writers*perWriter)
	}
}

func TestReadAuditMissingFile(t *testing.T) {
	entries, err := ReadAudit(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil || entries != nil {
		t.Fatalf("missing file: entries=%v err=%v", entries, err)
	}
}
