package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const astEditSampleGo = `package sample

// 保留这段中文注释。
func old(a int) int { return a + 1 }

func keep() {}
`

func TestRegisterAstEditNotInDefaultGolden(t *testing.T) {
	r := New(t.TempDir(), true)

	// 默认 golden 不含 ast_edit。
	for _, name := range r.Names() {
		if name == astEditToolName {
			t.Fatal("默认注册表不应包含 ast_edit")
		}
	}
	before := r.Names()

	RegisterAstEdit(r)
	once := r.Names()
	if len(once) != len(before)+1 {
		t.Fatalf("注册后数量 = %d，期望 %d", len(once), len(before)+1)
	}
	if once[len(once)-1] != astEditToolName {
		t.Fatalf("ast_edit 应追加在末尾，实际末尾 %q", once[len(once)-1])
	}
	for i := range before {
		if once[i] != before[i] {
			t.Fatalf("既有顺序被改变：第 %d 项 %q != %q", i, once[i], before[i])
		}
	}

	// 幂等：重复注册不再插入。
	RegisterAstEdit(r)
	if got := r.Names(); len(got) != len(once) {
		t.Fatalf("重复注册后数量 = %d，期望 %d", len(got), len(once))
	}
	// nil 是空操作。
	RegisterAstEdit(nil)

	// Specs 应包含该工具，且 schema 带意图字段（Specs 兜底注入）。
	found := false
	for _, spec := range r.Specs() {
		if spec.Function.Name == astEditToolName {
			found = true
			if !strings.Contains(string(spec.Function.Parameters), `"i"`) {
				t.Fatal("Specs 中的 ast_edit schema 应带 i 意图字段")
			}
		}
	}
	if !found {
		t.Fatal("Specs 应包含 ast_edit")
	}
}

func TestAstEditPreviewDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(astEditSampleGo), 0o644); err != nil {
		t.Fatal(err)
	}

	// 非 yolo 注册表：预览是非变更性的，必须仍能运行。
	r := New(dir, false)
	RegisterAstEdit(r)
	out, err := r.Run(context.Background(), astEditToolName,
		`{"path":"sample.go","symbol":"old","replacement":"func fresh(a int) int { return a + 9 }"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-func old") || !strings.Contains(out, "+func fresh") {
		t.Fatalf("预览应返回 diff：\n%s", out)
	}
	if !strings.Contains(out, "未写盘") {
		t.Fatalf("预览应标明未写盘：\n%s", out)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != astEditSampleGo {
		t.Fatalf("preview 不应写盘，文件内容被改动：\n%s", got)
	}
}

func TestAstEditRejectsNonGoFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(dir, true)
	RegisterAstEdit(r)
	_, err := r.Run(context.Background(), astEditToolName,
		`{"path":"notes.txt","symbol":"x","replacement":"y"}`)
	if err == nil || !strings.Contains(err.Error(), astEditNotGoMsg) {
		t.Fatalf("非 .go 文件应明确拒绝，实际 %v", err)
	}
}

func TestAstEditPreviewFalseWithoutWriterReturnsDiff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(astEditSampleGo), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(dir, true)
	RegisterAstEdit(r)
	out, err := r.Run(context.Background(), astEditToolName,
		`{"path":"sample.go","symbol":"old","replacement":"func fresh(a int) int { return a + 9 }","preview":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "apply_patch") || !strings.Contains(out, "+func fresh") {
		t.Fatalf("无写盘助手时应交回 diff 让模型用 apply_patch：\n%s", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != astEditSampleGo {
		t.Fatalf("无写盘助手时不应写盘：\n%s", got)
	}
}

func TestAstEditWriterWritesWhenPreviewFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte(astEditSampleGo), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(dir, true) // 注入写盘助手后工具变为变更性，需要 yolo。
	RegisterAstEdit(r)
	r.SetAstEditWriter(func(root, p string, content []byte) error {
		return os.WriteFile(filepath.Join(root, p), content, 0o644)
	})
	if !r.tools[astEditToolName].Mutating() {
		t.Fatal("注入写盘助手后 ast_edit 应为变更性")
	}

	out, err := r.Run(context.Background(), astEditToolName,
		`{"path":"sample.go","symbol":"old","replacement":"func fresh(a int) int { return a + 9 }","preview":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rewrote symbol") {
		t.Fatalf("写盘结果文案不符：\n%s", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "func fresh(a int) int { return a + 9 }") {
		t.Fatalf("写盘内容不符：\n%s", got)
	}
	if !strings.Contains(string(got), "// 保留这段中文注释。") {
		t.Fatalf("写盘后 CJK 注释丢失：\n%s", got)
	}
}
