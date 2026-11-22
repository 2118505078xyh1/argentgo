package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchContextDisambiguatesDuplicates(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	const original = "func alpha() {\n\treturn 1\n}\n\nfunc beta() {\n\treturn 1\n}\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// 两段相同的旧片段，用不同的 @@ 提示分别锚定。
	patch := "*** Begin Patch\n*** Update File: main.go\n@@ alpha\n-\treturn 1\n+\treturn 100\n@@ beta\n-\treturn 1\n+\treturn 200\n*** End Patch\n"
	raw, _ := json.Marshal(map[string]any{"input": patch})
	if _, err := (applyPatchTool{}).Run(context.Background(), root, raw); err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "func alpha() {\n\treturn 100\n}") {
		t.Fatalf("alpha not patched:\n%s", text)
	}
	if !strings.Contains(text, "func beta() {\n\treturn 200\n}") {
		t.Fatalf("beta not patched:\n%s", text)
	}
}

func TestApplyPatchWithoutContextStillAmbiguous(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "main.go")
	const original = "return 1\nreturn 1\n"
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "*** Begin Patch\n*** Update File: main.go\n@@\n-return 1\n+return 2\n*** End Patch\n"
	raw, _ := json.Marshal(map[string]any{"input": patch})
	if _, err := (applyPatchTool{}).Run(context.Background(), root, raw); err == nil {
		t.Fatal("expected ambiguity error without context")
	}
}

func TestParsePatchHunkHeaderKeepsContext(t *testing.T) {
	h, err := parsePatchHunkHeader("@@ alpha section", 1)
	if err != nil {
		t.Fatal(err)
	}
	if h.Context != "alpha section" {
		t.Fatalf("context = %q", h.Context)
	}
	h2, err := parsePatchHunkHeader("@@ -1,2 +1,2 @@ beta", 1)
	if err != nil {
		t.Fatal(err)
	}
	if h2.Context != "beta" || h2.OldStart != 1 {
		t.Fatalf("numeric header context = %q oldStart=%d", h2.Context, h2.OldStart)
	}
}
