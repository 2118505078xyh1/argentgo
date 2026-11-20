package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runWriteTool invokes the registered "write" tool with yolo enabled so the
// mutating-tool gate does not interfere with these filesystem assertions.
func runWriteTool(t *testing.T, root, rel, content string) (string, error) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{rel, content})
	if err != nil {
		t.Fatal(err)
	}
	return New(root, true).Run(context.Background(), "write", string(raw))
}

// setStaleModTime pins a file's modTime to a fixed point in the distant past.
// A real write then moves it to "now", so the two cases are distinguishable
// regardless of the filesystem's modTime resolution.
func setStaleModTime(t *testing.T, path string) {
	t.Helper()
	old := time.Unix(1_000_000, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func modTime(t *testing.T, path string) time.Time {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

func TestWriteToolSkipsWriteWhenContentUnchanged(t *testing.T) {
	root := t.TempDir()
	rel := "same.txt"
	path := filepath.Join(root, rel)
	if err := os.WriteFile(path, []byte("stable"), 0o644); err != nil {
		t.Fatal(err)
	}
	setStaleModTime(t, path)
	before := modTime(t, path)

	out, err := runWriteTool(t, root, rel, "stable")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(out, "no write needed") {
		t.Fatalf("expected a no-op result, got %q", out)
	}
	if after := modTime(t, path); !after.Equal(before) {
		t.Fatalf("unchanged write touched the file: modTime %v -> %v", before, after)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "stable" {
		t.Fatalf("content = %q, want %q", data, "stable")
	}
}

func TestWriteToolWritesWhenContentDiffers(t *testing.T) {
	root := t.TempDir()
	rel := "changed.txt"
	path := filepath.Join(root, rel)
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	setStaleModTime(t, path)
	before := modTime(t, path)

	out, err := runWriteTool(t, root, rel, "new")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.HasPrefix(out, "wrote ") {
		t.Fatalf("expected a normal write result, got %q", out)
	}
	if after := modTime(t, path); !after.After(before) {
		t.Fatalf("changed write did not update modTime: %v -> %v", before, after)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("content = %q, want %q", data, "new")
	}
}

func TestWriteToolCreatesMissingFile(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join("nested", "new.txt")

	out, err := runWriteTool(t, root, rel, "fresh")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.HasPrefix(out, "wrote ") {
		t.Fatalf("expected a normal write result, got %q", out)
	}
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "fresh" {
		t.Fatalf("content = %q, want %q", data, "fresh")
	}
}
