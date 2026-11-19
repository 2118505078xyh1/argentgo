package gui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMemoryCRUDSearchAndRedacts(t *testing.T) {
	dir := t.TempDir()
	memory := NewFileMemory(dir)

	if err := memory.Add("project", "editor", "use vim"); err != nil {
		t.Fatalf("Add editor: %v", err)
	}
	if err := memory.Add("user", "lang", "prefer Go"); err != nil {
		t.Fatalf("Add lang: %v", err)
	}
	if err := memory.Add("project", "token-holder", "api_key=sk-testvalue1234567890 keep me"); err != nil {
		t.Fatalf("Add secret: %v", err)
	}

	rows, err := memory.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("List = %d rows, want 3: %+v", len(rows), rows)
	}
	if rows[0].Scope != "project" || rows[0].Key != "editor" || rows[0].Value != "use vim" {
		t.Fatalf("first row = %+v", rows[0])
	}
	if rows[0].Source == nil || !strings.HasPrefix(*rows[0].Source, "memory/project__editor.md") {
		t.Fatalf("source = %v", rows[0].Source)
	}
	// List is sorted by scope then key; find the secret row explicitly rather
	// than depending on insertion position.
	var secret string
	found := false
	for _, row := range rows {
		if row.Key == "token-holder" {
			secret, found = row.Value, true
		}
	}
	if !found {
		t.Fatalf("token-holder row missing: %+v", rows)
	}
	// Credential-shaped values never leave the store in a snippet.
	if strings.Contains(secret, "sk-testvalue1234567890") || !strings.Contains(secret, "[redacted:secret]") {
		t.Fatalf("value was not redacted: %q", secret)
	}

	matches, err := memory.Search("PREFER")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 || matches[0].Key != "lang" || matches[0].Value != "prefer Go" {
		t.Fatalf("case-insensitive Search = %+v", matches)
	}
	matches, err = memory.Search("vim")
	if err != nil || len(matches) != 1 || matches[0].Key != "editor" {
		t.Fatalf("value Search = %+v, %v", matches, err)
	}

	if err := memory.Forget("project", "editor"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if err := memory.Forget("project", "editor"); err != nil {
		t.Fatalf("Forget is not idempotent: %v", err)
	}
	rows, err = memory.List()
	if err != nil || len(rows) != 2 {
		t.Fatalf("List after Forget = %+v, %v", rows, err)
	}
	for _, row := range rows {
		if row.Key == "editor" {
			t.Fatalf("forgotten key still present: %+v", row)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "memory", "project__editor.md")); !os.IsNotExist(err) {
		t.Fatalf("memory file still on disk: %v", err)
	}
}

func TestMemoryCommandsUseTheStorageDirDefault(t *testing.T) {
	dir := t.TempDir()
	env := newTestEnv(t, Options{StorageDir: dir})

	var rows []MemoryRow
	status, data := env.rpc("list_memory", "")
	if status != http.StatusOK {
		t.Fatalf("list_memory = %d %s", status, data)
	}
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 0 {
		t.Fatalf("empty list_memory = %s, %v", data, err)
	}

	if message := env.commandError("add_memory", `{"scope":"user","key":"color","value":"blue"}`); message != "" {
		t.Fatalf("add_memory error = %q", message)
	}
	status, data = env.rpc("list_memory", "")
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 || rows[0].Value != "blue" {
		t.Fatalf("list_memory after add = %s, %v", data, err)
	}

	status, data = env.rpc("search_memory", `{"query":"BLUE"}`)
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("search_memory = %s, %v", data, err)
	}
	if message := env.commandError("forget_memory", `{"scope":"user","key":"color"}`); message != "" {
		t.Fatalf("forget_memory error = %q", message)
	}
}
