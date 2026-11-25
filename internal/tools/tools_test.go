package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"example.com/argentgo/internal/tools"
)

func TestMutatingToolNeedsYolo(t *testing.T) {
	root := t.TempDir()
	_, err := tools.New(root, false).Run(context.Background(), "write", `{"path":"a.txt","content":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "--yolo") {
		t.Fatalf("err = %v, want a --yolo hint", err)
	}
}

func TestWriteThenRead(t *testing.T) {
	root := t.TempDir()
	registry := tools.New(root, true)

	if _, err := registry.Run(context.Background(), "write", `{"path":"sub/a.txt","content":"one\ntwo\n"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := registry.Run(context.Background(), "read", `{"path":"sub/a.txt","offset":2,"limit":1}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != "two" {
		t.Fatalf("read = %q, want %q", out, "two")
	}
}

func TestPathsStayInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	registry := tools.New(root, true)
	_, err := registry.Run(context.Background(), "read", `{"path":"../outside.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err = %v, want an outside-workspace rejection", err)
	}
}

func TestEditRequiresUniqueMatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "x.txt")
	if err := os.WriteFile(path, []byte("a a b"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.New(root, true)
	_, err := registry.Run(context.Background(), "edit", `{"path":"x.txt","old":"a","new":"c"}`)
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("err = %v, want a uniqueness error", err)
	}
	if _, err := registry.Run(context.Background(), "edit", `{"path":"x.txt","old":"b","new":"c"}`); err != nil {
		t.Fatalf("edit: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "a a c" {
		t.Fatalf("content = %q, want %q", data, "a a c")
	}
}

func TestGrepReportsLines(t *testing.T) {
	root := t.TempDir()
	registry := tools.New(root, true)
	if _, err := registry.Run(context.Background(), "write", `{"path":"notes.txt","content":"alpha\nbeta needle\ngamma\n"}`); err != nil {
		t.Fatal(err)
	}
	out, err := registry.Run(context.Background(), "grep", `{"pattern":"needle"}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "notes.txt:2:") {
		t.Fatalf("grep = %q, want a notes.txt:2 hit", out)
	}
}
