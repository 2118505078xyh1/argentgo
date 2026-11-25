package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/session"
)

func TestCompactionTodoSnapshot(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, _ := store.NewSession("compact-todo")
	messages := compactionHookMessages()
	for _, m := range messages {
		_ = sess.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID})
	}

	todos := `{"todos":[{"id":"1","text":"done item","status":"completed"},{"id":"2","text":"active item","status":"in_progress"},{"id":"3","text":"todo item","status":"pending"}]}`
	if err := os.WriteFile(filepath.Join(sess.Dir(), "todos.json"), []byte(todos), 0o644); err != nil {
		t.Fatal(err)
	}

	comp := newSessionCompactor(res, sess)
	comp.env = func(string) string { return "1" }
	out, changed := comp.MaybeCompact(messages, 150)
	if !changed {
		t.Fatal("expected compaction")
	}
	if !strings.Contains(out[0].Content, "[x] done item") ||
		!strings.Contains(out[0].Content, "[>] active item") ||
		!strings.Contains(out[0].Content, "[ ] todo item") {
		t.Fatalf("todo lines missing from summary:\n%s", out[0].Content)
	}

	records, _ := sess.Records()
	var found *session.Compaction
	for i := range records {
		if records[i].Kind == session.KindCompaction {
			var c session.Compaction
			if err := json.Unmarshal(records[i].Data, &c); err == nil {
				found = &c
			}
		}
	}
	if found == nil || len(found.Todo) != 3 {
		t.Fatalf("Compaction.Todo not persisted: %+v", found)
	}
}

func TestCompactionTodoOffLeavesRecordUnchanged(t *testing.T) {
	env, _ := testEnv(t)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t, res)
	sess, _ := store.NewSession("compact-todo-off")
	messages := compactionHookMessages()
	for _, m := range messages {
		_ = sess.Append(m.Role, map[string]any{"content": m.Content, "tool_call_id": m.ToolCallID})
	}
	if err := os.WriteFile(filepath.Join(sess.Dir(), "todos.json"), []byte(`{"todos":[{"text":"item","status":"pending"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	comp := newSessionCompactor(res, sess)
	comp.env = func(string) string { return "0" }
	if _, changed := comp.MaybeCompact(messages, 150); !changed {
		t.Fatal("expected compaction")
	}
	records, _ := sess.Records()
	for i := range records {
		if records[i].Kind == session.KindCompaction && strings.Contains(string(records[i].Data), `"todo"`) {
			t.Fatal("todo field should be absent when gate is off")
		}
	}
}
