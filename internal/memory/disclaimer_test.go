package memory

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBlockIncludesDisclaimer 确认两条注入路径都在头部带上启发式声明。
func TestBlockIncludesDisclaimer(t *testing.T) {
	docs := []Doc{{Path: "/repo/AGENTS.md", Text: "rule"}}
	for name, render := range map[string]func([]Doc) string{
		"Block":     Block,
		"BlockSafe": BlockSafe,
	} {
		block := render(docs)
		if !strings.Contains(block, Disclaimer) {
			t.Errorf("%s 缺少免责声明: %q", name, block)
		}
		if !strings.Contains(block, "most specific first") {
			t.Errorf("%s 缺少排序说明: %q", name, block)
		}
		if strings.Index(block, Disclaimer) < strings.Index(block, "most specific first") {
			t.Errorf("%s 声明应在排序说明之后: %q", name, block)
		}
	}
}

// TestBlockEmptyOmitsDisclaimer 确认空输入仍不渲染任何内容。
func TestBlockEmptyOmitsDisclaimer(t *testing.T) {
	if got := Block(nil); got != "" {
		t.Errorf("Block(nil) = %q, want empty", got)
	}
	if got := BlockSafe(nil); got != "" {
		t.Errorf("BlockSafe(nil) = %q, want empty", got)
	}
}

// TestBlockDisclaimerCJK 确认 CJK 路径与正文以及声明本身原样渲染且 UTF-8 有效。
func TestBlockDisclaimerCJK(t *testing.T) {
	docs := []Doc{
		{Path: "/项目/说明.md", Text: "中文指令 — 保留"},
		{Path: "/repo/AGENTS.md", Text: "english rule"},
	}
	for name, render := range map[string]func([]Doc) string{
		"Block":     Block,
		"BlockSafe": BlockSafe,
	} {
		block := render(docs)
		if !utf8.ValidString(block) {
			t.Errorf("%s 输出不是有效 UTF-8", name)
		}
		for _, want := range []string{"/项目/说明.md", "中文指令 — 保留", Disclaimer} {
			if !strings.Contains(block, want) {
				t.Errorf("%s 输出缺少 %q: %q", name, want, block)
			}
		}
	}
}

// TestBlockDoesNotMutateInput 确认渲染不会改动传入的 docs。
func TestBlockDoesNotMutateInput(t *testing.T) {
	docs := []Doc{
		{Path: "/a/AGENTS.md", Text: "a", Truncated: true},
		{Path: "/b/CLAUDE.md", Text: "b"},
	}
	before := append([]Doc(nil), docs...)
	_ = Block(docs)
	if !reflect.DeepEqual(docs, before) {
		t.Errorf("Block 改动了入参: got %#v, want %#v", docs, before)
	}
	_ = BlockSafe(docs)
	if !reflect.DeepEqual(docs, before) {
		t.Errorf("BlockSafe 改动了入参: got %#v, want %#v", docs, before)
	}
}

// TestBlockStable 确认重复渲染结果逐字节一致。
func TestBlockStable(t *testing.T) {
	docs := []Doc{{Path: "/repo/AGENTS.md", Text: "stable\nline"}}
	first := Block(docs)
	firstSafe := BlockSafe(docs)
	for i := 0; i < 5; i++ {
		if got := Block(docs); got != first {
			t.Fatalf("Block 第 %d 次渲染不稳定", i)
		}
		if got := BlockSafe(docs); got != firstSafe {
			t.Fatalf("BlockSafe 第 %d 次渲染不稳定", i)
		}
	}
}

// TestBlockBudgetPreserved 确认经 LoadIn 裁剪后送入 Block 的文档仍受
// 单文件 8KB 与总量 32KB 约束，且渲染出的块包含声明。
func TestBlockBudgetPreserved(t *testing.T) {
	base := t.TempDir()
	current := base
	for i := 0; i < 3; i++ {
		if err := os.MkdirAll(current, 0o755); err != nil {
			t.Fatal(err)
		}
		body := strings.Repeat("x", DefaultLimit+500)
		for _, name := range FileNames {
			if err := os.WriteFile(filepath.Join(current, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if i < 2 {
			current = filepath.Join(current, "d")
		}
	}
	docs, err := LoadIn(current, base, DefaultLimit)
	if err != nil {
		t.Fatalf("LoadIn: %v", err)
	}
	total := 0
	for _, doc := range docs {
		if len(doc.Text) > DefaultLimit+len(TruncationMarker) {
			t.Errorf("文档 %q 超出单文件上限: %d", doc.Path, len(doc.Text))
		}
		total += len(doc.Text)
	}
	if total > totalBudget {
		t.Errorf("合计 %d 字节超出总量上限 %d", total, totalBudget)
	}
	if block := Block(docs); !strings.Contains(block, Disclaimer) {
		t.Errorf("预算内的块缺少声明: %q", block)
	}
}
