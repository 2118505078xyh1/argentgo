package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProjectRootFindsNearestGitDir(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	outer := filepath.Join(home, "outer")
	inner := filepath.Join(outer, "src")
	deep := filepath.Join(inner, "pkg")
	if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outer, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 公开 API：停在最近的标记（inner/.git），而不是外层的。
	if got, want := ProjectRoot(deep), inner; got != want {
		t.Fatalf("ProjectRoot(%q) = %q, want %q", deep, got, want)
	}
	// 可测试的核心遵循显式的 home 边界。
	if got, want := projectRoot(deep, home, nil), inner; got != want {
		t.Fatalf("projectRoot = %q, want %q", got, want)
	}
}

func TestProjectRootHandlesGitFilePointer(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "worktree")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /some/where\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := projectRoot(filepath.Join(root, "src"), home, nil), root; got != want {
		t.Fatalf("projectRoot = %q, want %q (a .git file counts)", got, want)
	}
}

func TestProjectRootReturnsEmptyWithNoMarker(t *testing.T) {
	base := t.TempDir()
	const uniqueMarker = "NO_SUCH_PROJECT_MARKER_9f3a"
	if got := projectRoot(base, base, []string{uniqueMarker}); got != "" {
		t.Fatalf("projectRoot = %q, want empty when no marker exists", got)
	}
}

func TestProjectRootStopsAtHome(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	child := filepath.Join(home, "child")
	// home 之上存在标记；遍历必须停在 home 处而看不到它。
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := projectRoot(child, home, nil); got != "" {
		t.Fatalf("projectRoot = %q, want empty (marker above home must be ignored)", got)
	}
}

func TestProjectRootCustomMarkers(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".myroot"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "sub")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := projectRoot(dir, home, []string{".myroot"}), root; got != want {
		t.Fatalf("projectRoot = %q, want %q", got, want)
	}
	// 默认值不包含自定义标记。
	if got := projectRoot(dir, home, nil); got != "" {
		t.Fatalf("projectRoot with defaults = %q, want empty", got)
	}
}

func TestLoadWithBoundaryUntrustedSkipsProjectDocs(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "proj")
	dir := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "AGENTS.md", "project rules")
	writeFile(t, dir, "AGENTS.md", "nested project rules")
	writeFile(t, home, "AGENTS.md", "user rules")

	insts, err := LoadWithBoundary(dir, false, LoadOptions{Home: home})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("insts = %#v, want only the user-level doc", insts)
	}
	if insts[0].Text != "user rules" {
		t.Errorf("text = %q, want user rules", insts[0].Text)
	}
	if want := filepath.Join(home, "AGENTS.md"); insts[0].Source != want {
		t.Errorf("source = %q, want %q", insts[0].Source, want)
	}
}

func TestLoadWithBoundaryTrustedLoadsRootToCwdInOrder(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "proj")
	dir := filepath.Join(root, "sub")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "AGENTS.md", "root rules")
	writeFile(t, dir, "AGENTS.md", "sub rules")

	insts, err := LoadWithBoundary(dir, true, LoadOptions{Home: home})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("insts = %#v, want 2", insts)
	}
	if insts[0].Text != "root rules" || insts[1].Text != "sub rules" {
		t.Fatalf("order = %q, %q; want root then cwd", insts[0].Text, insts[1].Text)
	}
	if insts[0].Source != filepath.Join(root, "AGENTS.md") ||
		insts[1].Source != filepath.Join(dir, "AGENTS.md") {
		t.Errorf("sources = %q, %q", insts[0].Source, insts[1].Source)
	}
}

func TestLoadWithBoundaryOverridePrecedesAgentsAndKeepsClaude(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "AGENTS.md", "plain agents")
	writeFile(t, root, "AGENTS.override.md", "override agents")
	writeFile(t, root, "CLAUDE.md", "claude rules")

	insts, err := LoadWithBoundary(root, true, LoadOptions{Home: home})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("insts = %#v, want override + claude", insts)
	}
	if insts[0].Text != "override agents" {
		t.Errorf("text = %q, want override agents", insts[0].Text)
	}
	if want := filepath.Join(root, "AGENTS.override.md"); insts[0].Source != want {
		t.Errorf("source = %q, want %q", insts[0].Source, want)
	}
	if insts[1].Text != "claude rules" {
		t.Errorf("text = %q, want claude rules", insts[1].Text)
	}
	for _, in := range insts {
		if strings.Contains(in.Text, "plain agents") {
			t.Error("plain AGENTS.md must lose to AGENTS.override.md")
		}
	}
}

func TestLoadWithBoundaryPerFileCapIsRuneSafeCJK(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "AGENTS.md", "你好世界"+strings.Repeat("a", 64))

	insts, err := LoadWithBoundary(root, true, LoadOptions{Home: base, Limit: 7})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(insts) != 1 || !insts[0].Truncated {
		t.Fatalf("insts = %#v, want one truncated doc", insts)
	}
	body := strings.TrimSuffix(insts[0].Text, TruncationMarker)
	if !utf8.ValidString(body) {
		t.Fatalf("invalid UTF-8 after clipping: %q", body)
	}
	if !strings.HasPrefix(body, "你好") {
		t.Fatalf("body = %q, want CJK-safe prefix 你好", body)
	}
	if !strings.HasSuffix(insts[0].Text, TruncationMarker) {
		t.Errorf("truncation marker missing: %q", insts[0].Text)
	}
}

func TestLoadWithBoundaryTotalBudgetCapsCombinedText(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("x", DefaultLimit)
	dirs := []string{root}
	for i := 0; i < 5; i++ {
		next := filepath.Join(dirs[len(dirs)-1], "d")
		dirs = append(dirs, next)
	}
	for _, d := range dirs {
		writeFile(t, d, "AGENTS.md", content)
	}
	deepest := dirs[len(dirs)-1]

	insts, err := LoadWithBoundary(deepest, true, LoadOptions{Home: home})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	total := 0
	for _, in := range insts {
		total += len(in.Text)
	}
	if total > totalBudget {
		t.Errorf("combined text = %d bytes, budget = %d", total, totalBudget)
	}
	if len(insts) >= len(dirs) {
		t.Errorf("loaded %d docs for %d files; total budget should stop the walk", len(insts), len(dirs))
	}
	// 预算现在按从近到远消耗，因此被裁剪的文档是
	// 保留下来的最外层（根侧）文档，而不是最内层的 cwd 文档。
	if !insts[0].Truncated {
		t.Error("outermost kept doc should be marked truncated by the total budget")
	}
	if insts[len(insts)-1].Truncated {
		t.Error("nearest (cwd) doc should survive uncut when the budget is exhausted")
	}
}

// TestLoadWithBoundaryBudgetKeepsNearestDropsRoot 守护预算排序：
// 文件按从 cwd 到根目录的顺序读取，因此当 32 KiB 预算耗尽时，
// 最外层（根、最不具体）的指令被丢弃，而最靠近的
// （cwd、最具体的）指令保留。这是 "nearer wins" 契约，
// Block/LoadIn 承诺了它，但旧的自根向前的读取违反了它。
func TestLoadWithBoundaryBudgetKeepsNearestDropsRoot(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	root := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 从根到 cwd 共六个目录，每个持有一个 DefaultLimit 大小的文件，
	// 使 32 KiB 预算无法容纳全部：四个完整文件加一个
	// 被裁剪的文件可以容纳，根文件则从不读取。
	markers := []string{"ROOT_MARKER", "L1_MARKER", "L2_MARKER", "L3_MARKER", "L4_MARKER", "CWD_MARKER"}
	dirs := []string{root}
	for i := 0; i < len(markers)-1; i++ {
		dirs = append(dirs, filepath.Join(dirs[len(dirs)-1], fmt.Sprintf("d%d", i)))
	}
	for i, d := range dirs {
		writeFile(t, d, "AGENTS.md", markers[i]+strings.Repeat("x", DefaultLimit-len(markers[i])))
	}

	insts, err := LoadWithBoundary(dirs[len(dirs)-1], true, LoadOptions{Home: home})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	var joined strings.Builder
	for _, in := range insts {
		joined.WriteString(in.Text)
	}
	if !strings.Contains(joined.String(), "CWD_MARKER") {
		t.Errorf("nearest (cwd) instruction was dropped: %q", joined.String())
	}
	if strings.Contains(joined.String(), "ROOT_MARKER") {
		t.Errorf("outermost (root) instruction should have been dropped: %q", joined.String())
	}

	// 返回顺序保持从根到 cwd；保留的层级是 1..5。
	wantOrder := []string{"L1_MARKER", "L2_MARKER", "L3_MARKER", "L4_MARKER", "CWD_MARKER"}
	if len(insts) != len(wantOrder) {
		t.Fatalf("got %d docs, want %d: %#v", len(insts), len(wantOrder), insts)
	}
	for i, want := range wantOrder {
		if !strings.HasPrefix(insts[i].Text, want) {
			t.Errorf("insts[%d] = %q, want prefix %q", i, insts[i].Text, want)
		}
	}
}

func TestLoadWithBoundaryProvenancePathPresent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "CLAUDE.md", "be brief")

	insts, err := LoadWithBoundary(root, true, LoadOptions{Home: base})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := filepath.Join(root, "CLAUDE.md")
	if len(insts) != 1 || insts[0].Source != want {
		t.Fatalf("insts = %#v, want Source = %q", insts, want)
	}
	if docs := InstructionsToDocs(insts); len(docs) != 1 || docs[0].Path != want || docs[0].Text != "be brief" {
		t.Fatalf("InstructionsToDocs = %#v", docs)
	}
}

// instrAt 按 LoadWithBoundary 会生成的方式构造 Instruction：一个
// 指向 dir 中 AGENTS.md 的绝对 Source。文件无需存在 ——
// RelevantInstructions 是纯路径过滤器。
func instrAt(dir, text string) Instruction {
	return Instruction{Source: filepath.Join(dir, "AGENTS.md"), Text: text}
}

// textsOf 按顺序提取每条指令的 Text，用于简洁的断言。
func textsOf(insts []Instruction) []string {
	out := make([]string, len(insts))
	for i, in := range insts {
		out[i] = in.Text
	}
	return out
}

func TestRelevantInstructionsScopesNestedDocsByDirectory(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	parser := filepath.Join(src, "parser")
	lexer := filepath.Join(src, "lexer")

	// 从根到 cwd 的顺序，即 LoadWithBoundary 返回的顺序。
	docs := []Instruction{
		instrAt(base, "root"),
		instrAt(src, "src"),
		instrAt(parser, "parser"),
		instrAt(lexer, "lexer"),
	}

	got := textsOf(RelevantInstructions(docs, filepath.Join(parser, "foo.go")))
	want := []string{"root", "src", "parser"}
	if len(got) != len(want) {
		t.Fatalf("RelevantInstructions = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RelevantInstructions = %#v, want %#v", got, want)
		}
	}
}

func TestRelevantInstructionsExcludesSiblingsAndPrefixLookalikes(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	srcExtra := filepath.Join(base, "src-extra") // prefix of "src" plus junk
	srcTwo := filepath.Join(base, "src2")
	docs := []Instruction{
		instrAt(src, "src"),
		instrAt(srcExtra, "src-extra"),
		instrAt(srcTwo, "src2"),
	}

	// 完全匹配的兄弟子树：只有 src2 适用，绝不可以是 src 或 src-extra。
	if got := textsOf(RelevantInstructions(docs, filepath.Join(srcTwo, "foo.go"))); len(got) != 1 || got[0] != "src2" {
		t.Fatalf("sibling match = %#v, want [src2]", got)
	}

	// 名称仅仅以 "src" 开头的目录不得匹配 src。
	if got := textsOf(RelevantInstructions(docs, filepath.Join(srcExtra, "foo.go"))); len(got) != 1 || got[0] != "src-extra" {
		t.Fatalf("prefix lookalike = %#v, want [src-extra]", got)
	}
}

func TestRelevantInstructionsRootAppliesEverywhereDeepestIsLocal(t *testing.T) {
	base := t.TempDir()
	parser := filepath.Join(base, "src", "parser")
	docs := []Instruction{instrAt(base, "root"), instrAt(parser, "parser")}

	// 项目根目录下的文件只看到根指令。
	if got := textsOf(RelevantInstructions(docs, filepath.Join(base, "top.go"))); len(got) != 1 || got[0] != "root" {
		t.Fatalf("root file = %#v, want [root]", got)
	}
	// parser 子树深处的文件同时看到两者。
	if got := textsOf(RelevantInstructions(docs, filepath.Join(parser, "deep", "p.go"))); len(got) != 2 {
		t.Fatalf("deep file = %#v, want [root parser]", got)
	}
}

func TestRelevantInstructionsPreservesOrderAndDoesNotMutateInput(t *testing.T) {
	base := t.TempDir()
	matchA := instrAt(filepath.Join(base, "a"), "A")
	matchB := instrAt(filepath.Join(base, "b"), "B")
	other := instrAt(filepath.Join(base, "c"), "C")
	// 刻意不排序：输出必须遵循输入顺序，而非重新排名。
	docs := []Instruction{other, matchB, matchA}

	got := RelevantInstructions(docs, filepath.Join(base, "b", "x.go"))
	if len(got) != 1 || got[0].Text != "B" {
		t.Fatalf("got = %#v, want only B", got)
	}
	// 输入切片必须保持不变（纯函数）。
	if len(docs) != 3 || docs[0].Text != "C" || docs[1].Text != "B" || docs[2].Text != "A" {
		t.Fatalf("input was mutated: %#v", docs)
	}
}

func TestRelevantInstructionsCleansDotsAndSkipsEmptySource(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	parser := filepath.Join(src, "parser")
	lexer := filepath.Join(src, "lexer")
	docs := []Instruction{
		{Source: "", Text: "no source"},
		instrAt(parser, "parser"),
		instrAt(lexer, "lexer"),
	}

	// parser/../lexer 解析到 lexer 子树，而非 parser 子树。
	touched := filepath.Join(parser, "..", "lexer", "foo.go")
	got := textsOf(RelevantInstructions(docs, touched))
	if len(got) != 1 || got[0] != "lexer" {
		t.Fatalf("got = %#v, want [lexer]", got)
	}
}

func TestRelevantInstructionsEmptyInputsAndOutsideAllDirs(t *testing.T) {
	base := t.TempDir()
	docs := []Instruction{instrAt(filepath.Join(base, "src"), "src")}

	if got := RelevantInstructions(nil, filepath.Join(base, "src", "x.go")); got != nil {
		t.Fatalf("nil docs = %#v, want nil", got)
	}
	if got := RelevantInstructions(docs, ""); got != nil {
		t.Fatalf("empty path = %#v, want nil", got)
	}
	if got := RelevantInstructions(docs, "   "); got != nil {
		t.Fatalf("blank path = %#v, want nil", got)
	}
	// 不在任何指令目录下的路径不匹配任何内容。
	outside := filepath.Join(t.TempDir(), "x.go")
	if got := RelevantInstructions(docs, outside); got != nil {
		t.Fatalf("outside path = %#v, want nil", got)
	}
}

// TestRelevantInstructionsCaseInsensitiveOnWindows 守护文档中记录的
// Windows 上不区分大小写的包含关系；在其他路径比较区分大小写的地方
// 该断言会被跳过。
func TestRelevantInstructionsCaseInsensitiveOnWindows(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("case-insensitive path matching applies on Windows only")
	}
	base := t.TempDir()
	src := filepath.Join(base, "Src")
	docs := []Instruction{instrAt(src, "src")}
	touched := filepath.Join(strings.ToUpper(src), "Foo.go")
	if got := textsOf(RelevantInstructions(docs, touched)); len(got) != 1 || got[0] != "src" {
		t.Fatalf("got = %#v, want [src]", got)
	}
}

// TestLoadInBehaviourUnchanged 是回归守护：添加 boundary
// 加载器不得改变现有 nearest-first、home-stopping 的 LoadIn 路径，
// 也不得使其读取 AGENTS.override.md。
func TestLoadInBehaviourUnchanged(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	cwd := filepath.Join(home, "proj", "src")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, cwd, "AGENTS.md", "local rules")
	writeFile(t, cwd, "AGENTS.override.md", "override rules")
	writeFile(t, filepath.Join(home, "proj"), "CLAUDE.md", "project rules")
	writeFile(t, home, "AGENTS.md", "home rules")
	writeFile(t, base, "AGENTS.md", "above home rules")

	docs, err := LoadIn(cwd, home, DefaultLimit)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("docs = %#v, want 3", docs)
	}
	if docs[0].Text != "local rules" || docs[1].Text != "project rules" || docs[2].Text != "home rules" {
		t.Errorf("order = %#v", docs)
	}
	for _, doc := range docs {
		if strings.Contains(doc.Text, "override rules") || strings.Contains(doc.Text, "above home") {
			t.Errorf("LoadIn must ignore override files and stop at home: %#v", docs)
		}
	}
}
