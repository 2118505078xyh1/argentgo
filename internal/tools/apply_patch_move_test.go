package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchMoveTo(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "old.txt")
	if err := os.WriteFile(src, []byte("line one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	patch := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt\n@@\n-line one\n+line two\n*** End Patch\n"
	raw, _ := json.Marshal(map[string]any{"input": patch})
	if _, err := (applyPatchTool{}).Run(context.Background(), root, raw); err != nil {
		t.Fatalf("apply patch with move: %v", err)
	}

	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("old path should be gone, err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "line two\n" {
		t.Fatalf("moved content = %q", got)
	}
}

func TestApplyPatchMoveToExistingTargetRejected(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "old.txt"), []byte("x\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "new.txt"), []byte("already\n"), 0o644)
	patch := "*** Begin Patch\n*** Update File: old.txt\n*** Move to: new.txt\n*** End Patch\n"
	raw, _ := json.Marshal(map[string]any{"input": patch})
	if _, err := (applyPatchTool{}).Run(context.Background(), root, raw); err == nil {
		t.Fatal("move to existing target should be rejected")
	}
}

func TestParsePatchMoveTo(t *testing.T) {
	actions, err := ParseApplyPatch("*** Begin Patch\n*** Update File: a\n*** Move to: b\n*** End Patch\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].MoveTo != "b" {
		t.Fatalf("parsed actions = %+v", actions)
	}
}
