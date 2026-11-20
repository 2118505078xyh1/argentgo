package memory

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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

// TestHugeFileIsNotFullyRead 守护流式读取：上限很小时，
// 加载器分配的内存不得与超大文件的大小成正比。
func TestHugeFileIsNotFullyRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const size = 32 << 20 // 32 MiB
	chunk := bytes.Repeat([]byte("a"), 1<<20)
	for i := 0; i < size/len(chunk); i++ {
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	docs, err := LoadIn(root, root, 128)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 1 || !docs[0].Truncated {
		t.Fatalf("docs = %#v, want one truncated doc", docs)
	}
	if got, ceiling := after.TotalAlloc-before.TotalAlloc, uint64(size/4); got >= ceiling {
		t.Fatalf("allocated %d bytes for a %d-byte file; streaming read expected (< %d)", got, size, ceiling)
	}
}

// TestTotalBudgetCapsCombinedText 检查多个受限文件无法向
// 系统提示词推入无限量的文本。
func TestTotalBudgetCapsCombinedText(t *testing.T) {
	base := t.TempDir()
	root := base
	for i := 0; i < 7; i++ {
		root = filepath.Join(root, "d"+strconv.Itoa(i))
	}
	var dirs []string
	for d := root; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if d == base {
			break
		}
	}
	if len(dirs) != 8 {
		t.Fatalf("dirs = %d, want 8", len(dirs))
	}
	content := strings.Repeat("x", DefaultLimit)
	for _, d := range dirs {
		writeFile(t, d, "AGENTS.md", content)
	}

	docs, err := LoadIn(root, base, DefaultLimit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	total := 0
	for _, d := range docs {
		total += len(d.Text)
	}
	if total > totalBudget {
		t.Errorf("combined text = %d bytes, budget = %d", total, totalBudget)
	}
	if len(docs) >= len(dirs) {
		t.Errorf("loaded %d docs for %d files; budget should stop the walk", len(docs), len(dirs))
	}
	if !docs[len(docs)-1].Truncated {
		t.Error("last doc should be marked truncated by the global budget")
	}
}

// TestTruncationIsRuneSafe 检查裁剪从不拆分 UTF-8 rune，
// 包括 CJK 和 emoji 序列。
func TestTruncationIsRuneSafe(t *testing.T) {
	cases := []struct {
		name, content, wantPrefix string
		limit                     int
	}{
		{"cjk", "你好世界" + strings.Repeat("a", 64), "你好", 7},
		{"emoji", "ab😀cd" + strings.Repeat("a", 64), "ab", 4},
		{"exact boundary", "abc你好" + strings.Repeat("a", 64), "abc", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "AGENTS.md", tc.content)
			docs, err := LoadIn(root, root, tc.limit)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(docs) != 1 {
				t.Fatalf("docs = %#v", docs)
			}
			if !docs[0].Truncated {
				t.Fatal("want Truncated")
			}
			body := strings.TrimSuffix(docs[0].Text, TruncationMarker)
			if !utf8.ValidString(body) {
				t.Fatalf("invalid UTF-8 after truncation: %q", body)
			}
			if !strings.HasPrefix(body, tc.wantPrefix) {
				t.Fatalf("body %q does not start with %q", body, tc.wantPrefix)
			}
		})
	}
}

// TestNoLimitKeepsWholeFile 保留文档所述的 LoadLimit(limit<=0)
// 单文件"无上限"语义：文件被完整返回且无截断标记。
func TestNoLimitKeepsWholeFile(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("你好😀", 2000)
	writeFile(t, root, "AGENTS.md", content)
	docs, err := LoadIn(root, root, 0)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %#v", docs)
	}
	if docs[0].Truncated {
		t.Error("limit<=0 must not truncate")
	}
	if docs[0].Text != content {
		t.Error("limit<=0 must keep the whole file")
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
