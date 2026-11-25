package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/memory"
)

func TestMemoryToolMetadata(t *testing.T) {
	tools := []Tool{newMemoryRetainTool(nil), newMemoryRecallTool(nil), newMemoryForgetTool(nil)}
	wantNames := []string{"memory_retain", "memory_recall", "memory_forget"}
	wantMutating := []bool{true, false, true}
	for i, tool := range tools {
		if tool.Name() != wantNames[i] {
			t.Errorf("name %d = %q, want %q", i, tool.Name(), wantNames[i])
		}
		if tool.Mutating() != wantMutating[i] {
			t.Errorf("mutating %d = %v, want %v", i, tool.Mutating(), wantMutating[i])
		}
		if !json.Valid([]byte(tool.Schema())) {
			t.Errorf("schema %d is not valid JSON: %s", i, tool.Schema())
		}
		if len(tool.Description()) >= 1024 {
			t.Errorf("description %d is too long: %d bytes", i, len(tool.Description()))
		}
	}
}

func TestMemoryToolsRetainRecallForget(t *testing.T) {
	dir := t.TempDir()
	bank := memory.NewBank(dir)
	retain := newMemoryRetainTool(bank)
	recall := newMemoryRecallTool(bank)
	forget := newMemoryForgetTool(bank)
	ctx := context.Background()

	out, err := retain.Run(ctx, dir, json.RawMessage(`{"text":"Paris is the capital of France","tags":["geo"],"source":"unit test"}`))
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	if !strings.HasPrefix(out, "stored memory ") {
		t.Fatalf("retain output = %q, want a stored-id message", out)
	}
	id := strings.TrimPrefix(out, "stored memory ")

	out, err = recall.Run(ctx, dir, json.RawMessage(`{"query":"capital France","limit":5}`))
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(out, "Paris is the capital of France") {
		t.Fatalf("recall output = %q, want the retained fact", out)
	}

	if _, err := forget.Run(ctx, dir, json.RawMessage(`{"id":"`+id+`"}`)); err != nil {
		t.Fatalf("forget: %v", err)
	}
	out, err = recall.Run(ctx, dir, json.RawMessage(`{"query":"capital France","limit":5}`))
	if err != nil {
		t.Fatalf("recall after forget: %v", err)
	}
	if out != "no memories found" {
		t.Fatalf("recall after forget = %q, want empty result", out)
	}
}

func TestMemoryToolsHandleBadArguments(t *testing.T) {
	bank := memory.NewBank(t.TempDir())
	ctx := context.Background()
	retain := newMemoryRetainTool(bank)
	recall := newMemoryRecallTool(bank)
	forget := newMemoryForgetTool(bank)

	if _, err := retain.Run(ctx, "", json.RawMessage(`{"text":"   "}`)); err == nil {
		t.Fatal("retain with empty text must fail")
	}
	if _, err := recall.Run(ctx, "", json.RawMessage(`not json`)); err == nil {
		t.Fatal("recall with invalid JSON must fail")
	}
	if _, err := forget.Run(ctx, "", json.RawMessage(`{"id":""}`)); err == nil {
		t.Fatal("forget with empty id must fail")
	}
}

func TestMemoryToolsRegisteredAndOverridable(t *testing.T) {
	dir := t.TempDir()
	registry := New(dir, true)
	for _, name := range []string{"memory_retain", "memory_recall", "memory_forget"} {
		found := false
		for _, registered := range registry.Names() {
			if registered == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("tool %q not registered; names = %v", name, registry.Names())
		}
	}

	ctx := context.Background()
	if _, err := registry.Run(ctx, "memory_retain", `{"text":"default bank fact"}`); err != nil {
		t.Fatalf("default bank retain: %v", err)
	}
	defaultPath := filepath.Join(dir, ".argent", "memory.jsonl")
	if _, err := os.Stat(defaultPath); err != nil {
		t.Fatalf("default memory file missing: %v", err)
	}

	other := t.TempDir()
	registry.SetMemoryBank(memory.NewBank(other))
	if _, err := registry.Run(ctx, "memory_retain", `{"text":"override bank fact"}`); err != nil {
		t.Fatalf("override bank retain: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(other, "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "override bank fact") {
		t.Fatalf("override bank file = %q, want the new fact", data)
	}
	// tool_search 被追加在注册列表之后，因此记忆工具
	// 仍必须连续地紧邻它之前。SetMemoryBank 不得改变
	// 该顺序，即使它交换了每个工具背后的库。
	names := registry.Names()
	last := len(names) - 1
	wantTail := []string{"memory_retain", "memory_recall", "memory_forget", "memory_reflect", "memory_learn"}
	if names[last] != "tool_search" || len(names) < len(wantTail)+1 {
		t.Fatalf("SetMemoryBank changed registration order: %v", names)
	}
	for i, want := range wantTail {
		if names[last-len(wantTail)+i] != want {
			t.Fatalf("SetMemoryBank changed registration order: %v", names)
		}
	}
}

func TestMemoryMutatingToolsNeedYolo(t *testing.T) {
	registry := New(t.TempDir(), false)
	ctx := context.Background()
	for _, name := range []string{"memory_retain", "memory_forget"} {
		if _, err := registry.Run(ctx, name, `{}`); err == nil || !strings.Contains(err.Error(), "--yolo") {
			t.Fatalf("%s without yolo: err = %v, want --yolo hint", name, err)
		}
	}
	if _, err := registry.Run(ctx, "memory_recall", `{}`); err != nil {
		t.Fatalf("memory_recall must be read-only: %v", err)
	}
}
