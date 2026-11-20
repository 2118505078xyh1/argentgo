package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlanMultiEditClassifiesAndSimulatesInOrder(t *testing.T) {
	files := multiEditKnownFiles{
		"a.txt": "alpha\nbeta\ngamma\n",
		"b.txt": "one\ntwo\n",
	}
	blocks := []multiEditBlock{
		{Path: "a.txt", Old: "alpha", New: "ALPHA"}, // 0: literal, unique
		{Path: "a.txt", Old: "not here", New: "X"},  // 1: nowhere
		{Path: "a.txt", Old: "beta", New: "BETA"},   // 2: literal, unique
		{Path: "a.txt", Old: "beta", New: "?"},      // 3: consumed by block 2
		{Path: "a.txt", Old: "", New: "X"},          // 4: empty SEARCH
		{Path: "b.txt", Old: "one", New: "ONE"},     // 5: literal, unique
	}

	plan := planMultiEdit(blocks, multiEditSnapshotResolver{files: files})

	if plan.OK() {
		t.Fatal("plan.OK() = true, want false for a batch with failures")
	}
	if got, want := plan.Applicable, []int{0, 2, 5}; !equalInts(got, want) {
		t.Fatalf("Applicable = %v, want %v", got, want)
	}
	type wantFailure struct {
		idx  int
		kind multiEditFailureKind
	}
	wants := []wantFailure{
		{1, multiEditNotFound},
		{3, multiEditNotFound},
		{4, multiEditEmptySearch},
	}
	if len(plan.Failed) != len(wants) {
		t.Fatalf("Failed = %+v, want %d entries", plan.Failed, len(wants))
	}
	for i, want := range wants {
		got := plan.Failed[i]
		if got.Index != want.idx || got.Kind != want.kind {
			t.Fatalf("Failed[%d] = %+v, want index %d kind %q", i, got, want.idx, want.kind)
		}
	}
	// 成功的编辑对后续块可见；失败的则不可见。
	if got, want := plan.Snapshot["a.txt"], "ALPHA\nBETA\ngamma\n"; got != want {
		t.Fatalf("simulated a.txt = %q, want %q", got, want)
	}
	if got, want := plan.Snapshot["b.txt"], "ONE\ntwo\n"; got != want {
		t.Fatalf("simulated b.txt = %q, want %q", got, want)
	}
	// dry run 也不得改变调用方的快照。
	if got, want := files["a.txt"], "alpha\nbeta\ngamma\n"; got != want {
		t.Fatalf("input snapshot mutated: %q, want %q", got, want)
	}
}

func TestPlanMultiEditRejectsAmbiguousAndNeedsRepairBlocks(t *testing.T) {
	t.Run("not_unique", func(t *testing.T) {
		files := multiEditKnownFiles{"dup.txt": "dup\ndup\n"}
		plan := planMultiEdit([]multiEditBlock{{Path: "dup.txt", Old: "dup", New: "done"}},
			multiEditSnapshotResolver{files: files})
		if len(plan.Failed) != 1 || plan.Failed[0].Kind != multiEditNotUnique {
			t.Fatalf("Failed = %+v, want one not_unique entry", plan.Failed)
		}
		if len(plan.Applicable) != 0 {
			t.Fatalf("Applicable = %v, want none for an ambiguous match", plan.Applicable)
		}
	})

	t.Run("needs_repair", func(t *testing.T) {
		files := multiEditKnownFiles{"s.txt": "\tfoo()\n\tbar()\n"}
		plan := planMultiEdit([]multiEditBlock{{
			Path: "s.txt", Old: "    foo()\n    bar()", New: "    foo()\n    BAZ()",
		}}, multiEditSnapshotResolver{files: files})
		if len(plan.Failed) != 1 || plan.Failed[0].Kind != multiEditNeedsRepair {
			t.Fatalf("Failed = %+v, want one needs_repair entry", plan.Failed)
		}
		cands := plan.Failed[0].Candidates
		if len(cands) != 1 || cands[0].Path != "s.txt" || !cands[0].ViaRepair {
			t.Fatalf("Candidates = %+v, want one fuzzy candidate in s.txt", cands)
		}
		if got, want := files["s.txt"], "\tfoo()\n\tbar()\n"; got != want {
			t.Fatalf("fuzzy match was applied during dry run: %q", got)
		}
	})
}

func TestResolveBlockFileFindsUniqueLiteralInAnotherFile(t *testing.T) {
	known := multiEditKnownFiles{
		"src/main.go": "func hello() {}\n",
		"src/dup.go":  "func hello() {}\nfunc hello() {}\n", // two matches in one file: not a candidate
		"src/中文.go":   "func 你好() {}\n",                     // CJK path and content: no match
	}
	block := multiEditBlock{Path: "src/wrong.go", Old: "func hello() {}", New: "func hi() {}"}

	cands := resolveBlockFile(block, known)
	if len(cands) != 1 || cands[0].Path != "src/main.go" || cands[0].ViaRepair {
		t.Fatalf("candidates = %+v, want exactly the literal match in src/main.go", cands)
	}

	// 当两个文件各持有一个字面匹配时，二者不分主次：
	// 调用方必须决策，因为自动挑选会改错文件。
	known["src/other.go"] = "func hello() {}\n"
	cands = resolveBlockFile(block, known)
	if len(cands) != 2 || cands[0].Path != "src/main.go" || cands[1].Path != "src/other.go" {
		t.Fatalf("candidates = %+v, want both literal matches in path order", cands)
	}
}

func TestResolveBlockFileFindsFuzzyCandidateWithCJKPath(t *testing.T) {
	known := multiEditKnownFiles{"文档/说明.txt": "  第一行\n  第二行\n"}
	block := multiEditBlock{Path: "docs/readme.txt", Old: "第一行\n第二行", New: "第一行\n第三行"}

	cands := resolveBlockFile(block, known)
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, want one fuzzy candidate", cands)
	}
	if cands[0].Path != "文档/说明.txt" || !cands[0].ViaRepair {
		t.Fatalf("candidate = %+v, want ViaRepair match in the CJK-named file", cands[0])
	}
	if !utf8.ValidString(cands[0].Path) || strings.ContainsRune(cands[0].Path, utf8.RuneError) {
		t.Fatalf("candidate path is not valid UTF-8: %q", cands[0].Path)
	}
}

type diskEditResolver struct{ paths []string }

func (d diskEditResolver) Paths() []string { return d.paths }

func (d diskEditResolver) Content(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func TestPlanMultiEditRelocatesWrongFilenameWithoutWriting(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "文档")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	right := filepath.Join(dir, "正确.txt")
	wrong := filepath.Join(dir, "错误.txt")
	if err := os.WriteFile(right, []byte("apple\nbanana\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrong, []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := diskEditResolver{paths: []string{right, wrong}}

	plan := planMultiEdit([]multiEditBlock{
		{Path: right, Old: "apple", New: "APPLE"},   // applicable
		{Path: wrong, Old: "banana", New: "BANANA"}, // wrong filename: found in right
	}, resolver)

	if !equalInts(plan.Applicable, []int{0}) {
		t.Fatalf("Applicable = %v, want [0]", plan.Applicable)
	}
	if len(plan.Failed) != 1 || plan.Failed[0].Kind != multiEditWrongFile {
		t.Fatalf("Failed = %+v, want one wrong_file entry", plan.Failed)
	}
	cands := plan.Failed[0].Candidates
	if len(cands) != 1 || canonicalEditPath(cands[0].Path) != canonicalEditPath(right) || cands[0].ViaRepair {
		t.Fatalf("Candidates = %+v, want the literal match in 正确.txt", cands)
	}
	if got, want := plan.Snapshot[canonicalEditPath(right)], "APPLE\nbanana\n"; got != want {
		t.Fatalf("simulated 正确.txt = %q, want %q", got, want)
	}
	// 两个文件都未被触碰：即使使用 CJK 路径，计划也只是 dry run。
	if got, err := os.ReadFile(right); err != nil || string(got) != "apple\nbanana\n" {
		t.Fatalf("right file changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(wrong); err != nil || string(got) != "nothing here\n" {
		t.Fatalf("wrong file changed: %q, %v", got, err)
	}
}

func TestMultiEditBatchDiagnosticInstructionMatchesWriteSemantics(t *testing.T) {
	content := "alpha\nbeta\n"
	atomic := multiEditBatchDiagnostic("f.txt", 2, 3, 1, content, "beta", true)
	for _, want := range []string{"1 edit(s) matched cleanly in memory", "no bytes were written", "re-send the whole batch"} {
		if !strings.Contains(atomic, want) {
			t.Fatalf("atomic diagnostic %q missing %q", atomic, want)
		}
	}
	if strings.Contains(atomic, "do not re-send them") {
		t.Fatalf("atomic diagnostic tells the model to skip edits it never received: %q", atomic)
	}

	graded := multiEditBatchDiagnostic("f.txt", 2, 3, 1, content, "beta", false)
	for _, want := range []string{"were applied and written", "do not re-send them", "re-send only the failing edit(s)"} {
		if !strings.Contains(graded, want) {
			t.Fatalf("graded diagnostic %q missing %q", graded, want)
		}
	}
	if strings.Contains(graded, "re-send the whole batch") {
		t.Fatalf("graded diagnostic contradicts its own do-not-resend clause: %q", graded)
	}
}

func TestMultiEditCJKPathDefaultBehaviourUnchanged(t *testing.T) {
	root := t.TempDir()
	rel := "文档/笔记.txt"
	if err := os.MkdirAll(filepath.Join(root, "文档"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeFixture(t, root, rel, "中文第一行\n第二行\n")

	out, err := runMultiEditTool(t, root, rel, []map[string]any{
		{"old": "第二行", "new": "第二行已改"},
	}, false)
	if err != nil {
		t.Fatalf("multi_edit on a CJK path: %v", err)
	}
	if want := "edited " + rel + " (1 edits)"; out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	if got, want := readFixture(t, path), "中文第一行\n第二行已改\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if !utf8.ValidString(readFixture(t, path)) {
		t.Fatal("edited CJK content is not valid UTF-8")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
