package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeReadPathDropsLineSelectors(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"src/main.go", "src/main.go"},
		{"src/main.go:10", "src/main.go"},
		{"src/main.go:10:20", "src/main.go"},
		{"./src/main.go#12", "src/main.go"},
		{"/abs/a.go:3,7", "/abs/a.go"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeReadPath(tc.in); got != tc.want {
			t.Errorf("normalizeReadPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReadToolMetaUsesSupersedeKey(t *testing.T) {
	args := json.RawMessage(`{"path":"pkg/a.go:10:20"}`)
	meta := (readTool{}).Meta(args, "file body", nil)
	if meta.SupersedeKey != "read|pkg/a.go" {
		t.Fatalf("supersede key = %q, want %q", meta.SupersedeKey, "read|pkg/a.go")
	}
	if meta.Useless {
		t.Fatal("a successful read must not be marked useless")
	}
	if got := (readTool{}).Meta(args, "", context.DeadlineExceeded); got != (ToolMeta{}) {
		t.Fatalf("failed read meta = %+v, want empty", got)
	}
}

func TestSearchToolsMetaMarksOnlyEmptyResults(t *testing.T) {
	if !(grepTool{}).Meta(nil, "(no matches)", nil).Useless {
		t.Fatal("empty grep should be useless")
	}
	if (grepTool{}).Meta(nil, "a.go:1: hit", nil).Useless {
		t.Fatal("non-empty grep must not be useless")
	}
	if !(globTool{}).Meta(nil, `no files match "*.zzz"`, nil).Useless {
		t.Fatal("empty glob should be useless")
	}
	if (globTool{}).Meta(nil, "a.go\n", nil).Useless {
		t.Fatal("non-empty glob must not be useless")
	}
	if !(findTool{}).Meta(nil, "(no matches)", nil).Useless {
		t.Fatal("empty find should be useless")
	}
}

func TestRegistryRunWithMetaMatchesRunOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(root, true)
	args := `{"path":"a.go"}`

	plain, err := r.Run(context.Background(), "read", args)
	if err != nil {
		t.Fatal(err)
	}
	withMeta, meta, err := r.RunWithMeta(context.Background(), "read", args)
	if err != nil {
		t.Fatal(err)
	}
	if withMeta != plain {
		t.Fatalf("RunWithMeta changed the output: %q vs %q", withMeta, plain)
	}
	if meta.SupersedeKey != "read|a.go" {
		t.Fatalf("meta = %+v, want supersede key read|a.go", meta)
	}

	empty, _, err := r.RunWithMeta(context.Background(), "grep", `{"pattern":"does-not-exist"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, meta, err = r.RunWithMeta(context.Background(), "grep", `{"pattern":"does-not-exist"}`)
	if err != nil {
		t.Fatal(err)
	}
	if empty != "(no matches)" || !meta.Useless {
		t.Fatalf("empty grep output/meta = %q / %+v", empty, meta)
	}

	if _, meta, err := r.RunWithMeta(context.Background(), "read", `{"path":"missing.go"}`); err == nil || meta != (ToolMeta{}) {
		t.Fatalf("failed call should return empty meta: err=%v meta=%+v", err, meta)
	}
}
