package memory

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadForDirsDiscoversNestedAncestorsAndDedupes(t *testing.T) {
	base := t.TempDir()
	writeFile(t, base, "AGENTS.md", "root rules")
	writeFile(t, filepath.Join(base, "src"), "AGENTS.md", "src rules")
	writeFile(t, filepath.Join(base, "src", "parser"), "AGENTS.md", "parser rules")
	writeFile(t, filepath.Join(base, "src", "lexer"), "AGENTS.md", "lexer rules")

	// "src/parser" appears twice to exercise dedupe of active directories;
	// its ancestors are shared by both chains and must appear only once.
	docs := LoadForDirs(base, []string{"src/parser", "src/lexer", "src/parser"})
	got := make([]string, len(docs))
	for i, doc := range docs {
		got[i] = doc.Text
	}
	want := []string{"parser rules", "lexer rules", "src rules", "root rules"}
	if len(got) != len(want) {
		t.Fatalf("docs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("docs = %#v, want %#v", got, want)
		}
	}
}

func TestLoadForDirsSkipsFilesOutsideWorkspace(t *testing.T) {
	base := t.TempDir()
	writeFile(t, base, "AGENTS.md", "inside")
	outside := t.TempDir()
	writeFile(t, outside, "AGENTS.md", "outside")

	// Include a valid dir; the outside ones must be ignored, not fail the call.
	docs := LoadForDirs(base, []string{outside, "..", "", "."})
	if len(docs) != 1 || docs[0].Text != "inside" {
		t.Fatalf("docs = %#v, want only the in-workspace file", docs)
	}
}

func TestLoadForDirsClipsToTotalBudget(t *testing.T) {
	base := t.TempDir()
	deep := base
	for i := 0; i < 7; i++ {
		deep = filepath.Join(deep, "d"+strconv.Itoa(i))
	}
	for d := deep; filepath.Dir(d) != d; d = filepath.Dir(d) {
		writeFile(t, d, "AGENTS.md", strings.Repeat("x", DefaultLimit))
		if d == base {
			break
		}
	}
	rel, err := filepath.Rel(base, deep)
	if err != nil {
		t.Fatal(err)
	}

	docs := LoadForDirs(base, []string{rel})
	if len(docs) == 0 {
		t.Fatal("no docs loaded")
	}
	total := 0
	for _, doc := range docs {
		total += len(doc.Text)
	}
	if total > totalBudget {
		t.Errorf("combined text = %d bytes, budget = %d", total, totalBudget)
	}
	if !docs[len(docs)-1].Truncated {
		t.Error("last doc should be truncated by the global budget")
	}
}
