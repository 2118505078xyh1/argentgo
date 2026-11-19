package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalkUpIsNearestFirstAndStopsAtHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	cwd := filepath.Join(home, "project", "src")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cwd, "AGENTS.md", "local rules")
	writeFile(t, filepath.Join(home, "project"), "CLAUDE.md", "project rules")
	writeFile(t, home, "AGENTS.md", "home rules")
	writeFile(t, base, "AGENTS.md", "above home rules")

	docs, err := LoadIn(cwd, home, DefaultLimit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3: %#v", len(docs), docs)
	}
	if docs[0].Text != "local rules" || docs[1].Text != "project rules" || docs[2].Text != "home rules" {
		t.Errorf("order wrong: %#v", docs)
	}
	for _, doc := range docs {
		if strings.Contains(doc.Text, "above home") {
			t.Error("walk must stop at the home directory")
		}
	}
}

func TestTruncationCapsTextAndSetsFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "AGENTS.md", strings.Repeat("abcdefghij", 20))
	docs, err := LoadIn(root, root, 25)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %#v", docs)
	}
	if !docs[0].Truncated {
		t.Error("Truncated should be true")
	}
	if !strings.HasSuffix(docs[0].Text, TruncationMarker) {
		t.Errorf("marker missing: %q", docs[0].Text)
	}
	if body := strings.TrimSuffix(docs[0].Text, TruncationMarker); len(body) > 25 {
		t.Errorf("kept %d bytes, cap was 25", len(body))
	}
}

func TestShortFileIsNotTruncated(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "CLAUDE.md", "be brief")
	docs, err := LoadIn(root, root, DefaultLimit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 1 || docs[0].Truncated || docs[0].Text != "be brief" {
		t.Errorf("docs = %#v", docs)
	}
}

func TestBothFileNamesAreRead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "AGENTS.md", "from agents")
	writeFile(t, root, "CLAUDE.md", "from claude")
	docs, err := LoadIn(root, root, DefaultLimit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %#v", docs)
	}
	if docs[0].Text != "from agents" || docs[1].Text != "from claude" {
		t.Errorf("order = %#v", docs)
	}
}

func TestEmptyTreeLoadsNothing(t *testing.T) {
	root := t.TempDir()
	docs, err := LoadIn(root, root, DefaultLimit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("docs = %#v", docs)
	}
	if block := Block(docs); block != "" {
		t.Errorf("block = %q", block)
	}
}

func TestBlockRendersPathsAndOrderingNote(t *testing.T) {
	docs := []Doc{
		{Path: "/repo/src/AGENTS.md", Text: "local"},
		{Path: "/repo/AGENTS.md", Text: "root"},
	}
	block := Block(docs)
	if !strings.Contains(block, "most specific first") {
		t.Errorf("ordering note missing: %q", block)
	}
	if !strings.Contains(block, `<file path="/repo/src/AGENTS.md">`) {
		t.Errorf("path header missing: %q", block)
	}
	if !strings.Contains(block, "</project-memory>") {
		t.Errorf("closing tag missing: %q", block)
	}
	local := strings.Index(block, "local")
	root := strings.Index(block, "root")
	if local < 0 || root < 0 || local > root {
		t.Errorf("nearest file should render first: %q", block)
	}
}

func TestLoadRejectsFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIn(path, root, DefaultLimit); err == nil {
		t.Fatal("expected an error for a file root")
	}
	if _, err := LoadIn(filepath.Join(root, "missing"), root, DefaultLimit); err == nil {
		t.Fatal("expected an error for a missing root")
	}
}

func writeFile(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
