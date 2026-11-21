package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---- 测试辅助 ----

func applyPatchArgs(patch string) json.RawMessage {
	b, err := json.Marshal(map[string]string{"input": patch})
	if err != nil {
		panic(err)
	}
	return b
}

func runApplyPatch(t *testing.T, root, patch string) (string, error) {
	t.Helper()
	return applyPatchTool{}.Run(context.Background(), root, applyPatchArgs(patch))
}

func writeApplyPatchFixture(t *testing.T, root, name, content string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func readApplyPatchFixture(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// ---- 1. PatchHunk 的 oldText/newText（含导出别名）与 PatchAction 行为不变 ----

func TestPatchHunkOldTextNewText(t *testing.T) {
	h := PatchHunk{Lines: []string{" ctx", "-old", "+new", ""}}
	if got, want := h.oldText(), "ctx\nold\n"; got != want {
		t.Errorf("oldText() = %q, want %q", got, want)
	}
	if got, want := h.newText(), "ctx\nnew\n"; got != want {
		t.Errorf("newText() = %q, want %q", got, want)
	}
	if h.OldText() != h.oldText() || h.NewText() != h.newText() {
		t.Error("exported OldText/NewText aliases disagree with oldText/newText")
	}

	// 重构 hunkText 后 PatchAction 的扁平输出必须逐字节不变。
	a := PatchAction{Hunks: []PatchHunk{
		{Lines: []string{" a", "-b", "+c"}},
		{Lines: []string{" d", "+e"}},
	}}
	if got, want := a.OldText(), "a\nb\nd"; got != want {
		t.Errorf("PatchAction.OldText() = %q, want %q", got, want)
	}
	if got, want := a.NewText(), "a\nc\nd\ne"; got != want {
		t.Errorf("PatchAction.NewText() = %q, want %q", got, want)
	}
}

// ---- 2. 单 hunk 与多 hunk（跨 hunk 间隙保留）----

func TestApplyPatchSingleHunk(t *testing.T) {
	root := t.TempDir()
	p := writeApplyPatchFixture(t, root, "f.txt", "hello\nworld\n")

	patch := `*** Begin Patch
*** Update File: f.txt
@@
-hello
+HELLO
*** End Patch
`
	if _, err := runApplyPatch(t, root, patch); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := readApplyPatchFixture(t, p), "HELLO\nworld\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestApplyPatchMultipleHunksPreserveGap(t *testing.T) {
	root := t.TempDir()
	p := writeApplyPatchFixture(t, root, "f.txt", "line1\nline2\nline3\nline4\nline5\n")

	// 两个 hunk 分别改第 1、5 行；它们之间的 line3 必须原样保留。
	patch := `*** Begin Patch
*** Update File: f.txt
@@ -1,2 +1,2 @@
-line1
+LINE1
 line2
@@ -4,2 +4,2 @@
 line4
-line5
+LINE5
*** End Patch
`
	if _, err := runApplyPatch(t, root, patch); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := readApplyPatchFixture(t, p), "LINE1\nline2\nline3\nline4\nLINE5\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// ---- 3. add / delete ----

func TestApplyPatchAddAndDelete(t *testing.T) {
	root := t.TempDir()
	doomed := writeApplyPatchFixture(t, root, "old.txt", "bye\n")

	patch := `*** Begin Patch
*** Add File: dir/new.txt
+hello
+world
*** Delete File: old.txt
*** End Patch
`
	if _, err := runApplyPatch(t, root, patch); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := readApplyPatchFixture(t, filepath.Join(root, "dir", "new.txt")), "hello\nworld"; got != want {
		t.Fatalf("added content = %q, want %q", got, want)
	}
	if _, err := os.Stat(doomed); !os.IsNotExist(err) {
		t.Fatalf("deleted file still present (stat err = %v)", err)
	}
}

func TestApplyPatchAddBodyOnlyPlusLines(t *testing.T) {
	root := t.TempDir()
	patch := `*** Begin Patch
*** Add File: bad.txt
+good
 context
*** End Patch
`
	_, err := runApplyPatch(t, root, patch)
	if !errors.Is(err, ErrApplyPatch) {
		t.Fatalf("err = %v, want ErrApplyPatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "bad.txt")); !os.IsNotExist(statErr) {
		t.Fatal("add with non-'+' body must not create a file")
	}
}

// ---- 4. 混合批次原子性：中途失败零写入 ----

func TestApplyPatchMixedBatchFailureIsAtomic(t *testing.T) {
	root := t.TempDir()
	existing := writeApplyPatchFixture(t, root, "existing.txt", "keep\n")
	doomed := writeApplyPatchFixture(t, root, "doomed.txt", "old\n")

	patch := `*** Begin Patch
*** Add File: new.txt
+created
*** Update File: existing.txt
@@
-keep
+changed
*** Update File: doomed.txt
@@
-missing-anchor
+whatever
*** Delete File: doomed.txt
*** End Patch
`
	_, err := runApplyPatch(t, root, patch)
	if !errors.Is(err, ErrApplyPatchNotFound) {
		t.Fatalf("err = %v, want ErrApplyPatchNotFound", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(statErr) {
		t.Fatal("added file was written despite a later hunk failure")
	}
	if got, want := readApplyPatchFixture(t, existing), "keep\n"; got != want {
		t.Fatalf("existing content = %q, want unchanged %q", got, want)
	}
	if got, want := readApplyPatchFixture(t, doomed), "old\n"; got != want {
		t.Fatalf("doomed content = %q, want unchanged %q", got, want)
	}
}

func TestApplyPatchAmbiguousAnchorRejectedAtomically(t *testing.T) {
	root := t.TempDir()
	p := writeApplyPatchFixture(t, root, "dup.txt", "same\nsame\n")

	patch := `*** Begin Patch
*** Update File: dup.txt
@@
-same
+changed
*** End Patch
`
	_, err := runApplyPatch(t, root, patch)
	if !errors.Is(err, ErrApplyPatchAmbiguous) {
		t.Fatalf("err = %v, want ErrApplyPatchAmbiguous", err)
	}
	if got, want := readApplyPatchFixture(t, p), "same\nsame\n"; got != want {
		t.Fatalf("content = %q, want unchanged %q", got, want)
	}
}

// ---- 5. repairReplace 阶梯（尾随空白 / 缩进差异）----

func TestApplyPatchRepairTrailingWhitespace(t *testing.T) {
	root := t.TempDir()
	p := writeApplyPatchFixture(t, root, "ws.txt", "alpha  \nbeta\n")

	// 补丁锚点没有尾随空格，但文件里有；repairTrailingWhitespace 应恢复它。
	patch := `*** Begin Patch
*** Update File: ws.txt
@@
-alpha
-beta
+ALPHA
+BETA
*** End Patch
`
	if _, err := runApplyPatch(t, root, patch); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := readApplyPatchFixture(t, p), "ALPHA\nBETA\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestApplyPatchRepairIndent(t *testing.T) {
	root := t.TempDir()
	p := writeApplyPatchFixture(t, root, "indent.txt", "    foo\n    bar\n")

	// 补丁锚点无缩进，文件里有 4 空格缩进；repairIndent 应把缩进重新应用到新文本。
	patch := `*** Begin Patch
*** Update File: indent.txt
@@
-foo
-bar
+FOO
+BAR
*** End Patch
`
	if _, err := runApplyPatch(t, root, patch); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := readApplyPatchFixture(t, p), "    FOO\n    BAR\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// ---- 6. 路径逃逸（../ 与绝对路径）拒绝 ----

func TestApplyPatchRejectsRelativeEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "apply-patch-escape.txt")
	_ = os.Remove(outside)
	defer os.Remove(outside)

	patch := `*** Begin Patch
*** Add File: ../apply-patch-escape.txt
+pwned
*** End Patch
`
	_, err := runApplyPatch(t, root, patch)
	if !errors.Is(err, ErrApplyPatch) {
		t.Fatalf("err = %v, want ErrApplyPatch", err)
	}
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err text = %q, want boundary diagnostic", err.Error())
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatal("path escape created a file outside the workspace")
	}
}

func TestApplyPatchRejectsAbsolutePath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "abs.txt")

	patch := "*** Begin Patch\n*** Add File: " + filepath.ToSlash(outside) + "\n+pwned\n*** End Patch\n"
	_, err := runApplyPatch(t, root, patch)
	if !errors.Is(err, ErrApplyPatch) {
		t.Fatalf("err = %v, want ErrApplyPatch", err)
	}
	if !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err text = %q, want boundary diagnostic", err.Error())
	}
}

// ---- 7. Mutating 非 yolo 被 Registry.Run 拒绝 ----

func TestApplyPatchMutatingRequiresYolo(t *testing.T) {
	root := t.TempDir()
	patch := `*** Begin Patch
*** Add File: f.txt
+x
*** End Patch
`
	args := string(applyPatchArgs(patch))

	plain := New(root, false)
	RegisterApplyPatch(plain)
	_, err := plain.Run(context.Background(), "apply_patch", args)
	if err == nil || !strings.Contains(err.Error(), "rerun with --yolo") {
		t.Fatalf("non-yolo err = %v, want --yolo rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "f.txt")); !os.IsNotExist(statErr) {
		t.Fatal("non-yolo run must not create files")
	}

	yolo := New(root, true)
	RegisterApplyPatch(yolo)
	if _, err := yolo.Run(context.Background(), "apply_patch", args); err != nil {
		t.Fatalf("yolo run: %v", err)
	}
	if got, want := readApplyPatchFixture(t, filepath.Join(root, "f.txt")), "x"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// ---- 8. schema 合法性与工具契约 ----

func TestApplyPatchToolContractAndSchema(t *testing.T) {
	tool := applyPatchTool{}
	if got := tool.Name(); got != "apply_patch" {
		t.Fatalf("Name() = %q", got)
	}
	if !tool.Mutating() {
		t.Fatal("apply_patch must be mutating")
	}
	raw := []byte(tool.Schema())
	if !json.Valid(raw) {
		t.Fatalf("Schema() is not valid JSON: %s", raw)
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("type = %q, want object", schema.Type)
	}
	if !reflect.DeepEqual(schema.Required, []string{"input"}) {
		t.Errorf("required = %v, want [input]", schema.Required)
	}
	prop, ok := schema.Properties["input"]
	if !ok || prop.Type != "string" {
		t.Errorf("properties.input = %+v, want type string", prop)
	}
}

// ---- 9. RegisterApplyPatch 幂等 / 尾部追加 / 默认 golden 不变 ----

func TestRegisterApplyPatchIdempotentTailAndDefaultGolden(t *testing.T) {
	RegisterApplyPatch(nil) // nil 空操作，不应 panic

	// 默认注册表绝不包含 apply_patch（不进 builtin_register.go）。
	def := New(t.TempDir(), true)
	for _, name := range def.Names() {
		if name == "apply_patch" {
			t.Fatal("default New() must not contain apply_patch")
		}
	}

	r := New(t.TempDir(), true)
	before := r.Names()
	RegisterApplyPatch(r)
	after := r.Names()
	if len(after) != len(before)+1 {
		t.Fatalf("after len = %d, want %d", len(after), len(before)+1)
	}
	if !reflect.DeepEqual(after[:len(before)], before) {
		t.Fatal("existing tool order changed after registration")
	}
	if got := after[len(after)-1]; got != "apply_patch" {
		t.Fatalf("last tool = %q, want apply_patch", got)
	}
	RegisterApplyPatch(r) // 幂等
	if got := r.Names(); !reflect.DeepEqual(got, after) {
		t.Fatalf("second registration changed names: %v", got)
	}
}

// ---- 10. CJK 与 CRLF ----

func TestApplyPatchCJKAndCRLF(t *testing.T) {
	root := t.TempDir()
	p := writeApplyPatchFixture(t, root, "cjk.txt", "第一行\r\n第二行\r\n第三行\r\n")

	base := `*** Begin Patch
*** Update File: cjk.txt
@@
-第二行
+第二行修改
*** End Patch
`
	crlfPatch := strings.ReplaceAll(base, "\n", "\r\n")
	if _, err := runApplyPatch(t, root, crlfPatch); err != nil {
		t.Fatalf("CRLF run: %v", err)
	}
	got := readApplyPatchFixture(t, p)
	if !strings.Contains(got, "第二行修改\r\n") {
		t.Fatalf("CJK/CRLF content = %q", got)
	}
	if !strings.HasPrefix(got, "第一行\r\n") || !strings.HasSuffix(got, "第三行\r\n") {
		t.Fatalf("surrounding CRLF lines changed: %q", got)
	}

	// add 的 CJK 文件体来自 NewText()，以 LF 连接且保留 UTF-8。
	add := "*** Begin Patch\n*** Add File: 新增.txt\n+你好\n+世界\n*** End Patch\n"
	if _, err := runApplyPatch(t, root, add); err != nil {
		t.Fatalf("CJK add: %v", err)
	}
	if got, want := readApplyPatchFixture(t, filepath.Join(root, "新增.txt")), "你好\n世界"; got != want {
		t.Fatalf("added CJK content = %q, want %q", got, want)
	}
}

// ---- 11. 缺失文件与畸形输入错误 sentinel ----

func TestApplyPatchErrorsAreClassified(t *testing.T) {
	root := t.TempDir()
	missing := `*** Begin Patch
*** Update File: nope.txt
@@
-a
+b
*** End Patch
`
	_, err := runApplyPatch(t, root, missing)
	if !errors.Is(err, ErrPatchFileNotFound) || !errors.Is(err, ErrApplyPatch) {
		t.Fatalf("err = %v, want ErrPatchFileNotFound wrapping ErrApplyPatch", err)
	}

	_, err = runApplyPatch(t, root, "not a patch")
	if !errors.Is(err, ErrMalformedPatch) {
		t.Fatalf("err = %v, want ErrMalformedPatch", err)
	}

	// 空 patch 应被拒；这里用原始 Run 传空 JSON 以覆盖缺失 input 分支。
	_, err = applyPatchTool{}.Run(context.Background(), root, json.RawMessage(`{}`))
	if !errors.Is(err, ErrApplyPatch) {
		t.Fatalf("empty args err = %v, want ErrApplyPatch", err)
	}
}
