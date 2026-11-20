package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkBOMDoc asserts one loader result against the content budget. A leading
// BOM is metadata: the cap applies to the content bytes left after it is
// removed, not to the on-disk bytes that include it.
func checkBOMDoc(t *testing.T, text string, truncated, wantTruncated bool, wantText string) {
	t.Helper()
	body := text
	if truncated {
		body = strings.TrimSuffix(text, TruncationMarker)
		if body == text {
			t.Errorf("truncated doc is missing its marker: %q", text)
		}
	} else if strings.Contains(text, TruncationMarker) {
		t.Errorf("untruncated doc contains a truncation marker: %q", text)
	}
	if body != wantText {
		t.Errorf("body = %q, want %q", body, wantText)
	}
	if truncated != wantTruncated {
		t.Errorf("Truncated = %v, want %v", truncated, wantTruncated)
	}
}

// TestBOMDoesNotConsumePerFileByteBudget covers LoadIn and LoadWithBoundary,
// the two loaders with a configurable per-file cap. Before the fix the cap was
// charged against the raw bytes (BOM included), so a file whose content fit
// exactly in the limit lost its last three bytes and was marked truncated.
func TestBOMDoesNotConsumePerFileByteBudget(t *testing.T) {
	const bom = "\ufeff"
	cases := []struct {
		name      string
		body      string
		limit     int
		wantText  string
		wantTrunc bool
	}{
		{"content exactly at limit", strings.Repeat("a", 10), 10, strings.Repeat("a", 10), false},
		{"one content byte over limit", strings.Repeat("b", 11), 10, strings.Repeat("b", 10), true},
		{"multibyte clipped on rune boundary", "你好世界", 7, "你好", true},
		{"short content well under limit", "hello", 8, "hello", false},
		{"no BOM control", strings.Repeat("c", 10), 10, strings.Repeat("c", 10), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := bom
			if tc.name == "no BOM control" {
				prefix = ""
			}
			content := prefix + tc.body

			root := t.TempDir()
			writeFile(t, root, "AGENTS.md", content)
			docs, err := LoadIn(root, root, tc.limit)
			if err != nil {
				t.Fatalf("LoadIn: %v", err)
			}
			if len(docs) != 1 {
				t.Fatalf("LoadIn docs = %d, want 1: %#v", len(docs), docs)
			}
			checkBOMDoc(t, docs[0].Text, docs[0].Truncated, tc.wantTrunc, tc.wantText)

			root2 := t.TempDir()
			writeFile(t, root2, "AGENTS.md", content)
			if err := os.MkdirAll(filepath.Join(root2, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			insts, err := LoadWithBoundary(root2, true, LoadOptions{Home: t.TempDir(), Limit: tc.limit})
			if err != nil {
				t.Fatalf("LoadWithBoundary: %v", err)
			}
			if len(insts) != 1 {
				t.Fatalf("LoadWithBoundary insts = %d, want 1: %#v", len(insts), insts)
			}
			checkBOMDoc(t, insts[0].Text, insts[0].Truncated, tc.wantTrunc, tc.wantText)
		})
	}
}

// TestBOMDoesNotConsumeBudgetInLoadForDirs covers the nested loader, which
// always clips at DefaultLimit. The same BOM-before-cap contract must hold.
func TestBOMDoesNotConsumeBudgetInLoadForDirs(t *testing.T) {
	const bom = "\ufeff"
	cases := []struct {
		name      string
		body      string
		wantText  string
		wantTrunc bool
	}{
		{"exactly at DefaultLimit", strings.Repeat("a", DefaultLimit), strings.Repeat("a", DefaultLimit), false},
		{"three bytes over DefaultLimit", strings.Repeat("b", DefaultLimit+3), strings.Repeat("b", DefaultLimit), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "AGENTS.md", bom+tc.body)
			docs := LoadForDirs(root, []string{"."})
			if len(docs) != 1 {
				t.Fatalf("docs = %d, want 1: %#v", len(docs), docs)
			}
			checkBOMDoc(t, docs[0].Text, docs[0].Truncated, tc.wantTrunc, tc.wantText)
		})
	}
}
