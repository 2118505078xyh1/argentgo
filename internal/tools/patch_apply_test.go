package tools

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestApplySimplePatchReplaceSingle(t *testing.T) {
	tests := []struct {
		name     string
		original string
		patch    SimplePatch
		want     string
	}{
		{
			name:     "替换单词",
			original: "hello world",
			patch:    SimplePatch{Path: "a.txt", OldText: "world", NewText: "Go"},
			want:     "hello Go",
		},
		{
			name:     "替换为空串",
			original: "foo bar baz",
			patch:    SimplePatch{Path: "a.txt", OldText: " bar", NewText: ""},
			want:     "foo baz",
		},
		{
			name:     "在空原内容上以空旧文本插入",
			original: "",
			patch:    SimplePatch{Path: "a.txt", OldText: "", NewText: "fresh"},
			want:     "fresh",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplySimplePatch(tt.original, tt.patch)
			if err != nil {
				t.Fatalf("ApplySimplePatch() 意外错误: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ApplySimplePatch() = %q, 期望 %q", got, tt.want)
			}
		})
	}
}

func TestApplySimplePatchUniqueFailure(t *testing.T) {
	tests := []struct {
		name     string
		original string
		old      string
	}{
		{name: "零次匹配", original: "hello world", old: "missing"},
		{name: "多次匹配", original: "cat cat cat", old: "cat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ApplySimplePatch(tt.original, SimplePatch{Path: "a.txt", OldText: tt.old, NewText: "x"})
			if !errors.Is(err, ErrPatchNotUnique) {
				t.Fatalf("错误 = %v, 期望命中 ErrPatchNotUnique", err)
			}
			if got != "" {
				t.Fatalf("失败时返回内容 = %q, 期望空串", got)
			}
		})
	}
}

func TestApplySimplePatchAdd(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		got, err := ApplySimplePatch("", SimplePatch{Path: "new.txt", OldText: "", NewText: "内容", Add: true})
		if err != nil {
			t.Fatalf("ApplySimplePatch() 意外错误: %v", err)
		}
		if got != "内容" {
			t.Fatalf("ApplySimplePatch() = %q, 期望 %q", got, "内容")
		}
	})

	t.Run("原内容非空则拒绝", func(t *testing.T) {
		_, err := ApplySimplePatch("已有内容", SimplePatch{Path: "new.txt", OldText: "", NewText: "x", Add: true})
		if !errors.Is(err, ErrPatchAddNotEmpty) {
			t.Fatalf("错误 = %v, 期望命中 ErrPatchAddNotEmpty", err)
		}
	})
}

func TestApplySimplePatchDelete(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		got, err := ApplySimplePatch("保留删掉保留", SimplePatch{Path: "a.txt", OldText: "删掉", NewText: "", Delete: true})
		if err != nil {
			t.Fatalf("ApplySimplePatch() 意外错误: %v", err)
		}
		if got != "保留保留" {
			t.Fatalf("ApplySimplePatch() = %q, 期望 %q", got, "保留保留")
		}
	})

	t.Run("新文本非空则拒绝", func(t *testing.T) {
		_, err := ApplySimplePatch("abc", SimplePatch{Path: "a.txt", OldText: "b", NewText: "x", Delete: true})
		if !errors.Is(err, ErrPatchDeleteHasNew) {
			t.Fatalf("错误 = %v, 期望命中 ErrPatchDeleteHasNew", err)
		}
	})
}

func TestApplySimplePatchInvalidFlags(t *testing.T) {
	_, err := ApplySimplePatch("", SimplePatch{Path: "a.txt", OldText: "", NewText: "", Add: true, Delete: true})
	if !errors.Is(err, ErrPatchInvalidFlags) {
		t.Fatalf("错误 = %v, 期望命中 ErrPatchInvalidFlags", err)
	}
}

func TestApplySimplePatchCJKCRLF(t *testing.T) {
	original := "第一行\r\n第二行\r\n第三行\r\n"
	patch := SimplePatch{Path: "中文.txt", OldText: "第二行\r\n", NewText: "替换行\r\n"}
	want := "第一行\r\n替换行\r\n第三行\r\n"

	got, err := ApplySimplePatch(original, patch)
	if err != nil {
		t.Fatalf("ApplySimplePatch() 意外错误: %v", err)
	}
	if got != want {
		t.Fatalf("ApplySimplePatch() = %q, 期望 %q", got, want)
	}
	// CRLF 安全：每个 \n 前都应有 \r，且未被归一化成 LF。
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Fatalf("换行被破坏: %q", got)
	}
	// CJK 字节保持原样。
	for _, token := range []string{"第一行", "替换行", "第三行"} {
		if !strings.Contains(got, token) {
			t.Fatalf("结果丢失 CJK 文本 %q: %q", token, got)
		}
	}
}

func TestApplySimplePatchDoesNotMutateInput(t *testing.T) {
	original := "alpha beta gamma"
	patch := SimplePatch{Path: "a.txt", OldText: "beta", NewText: "BETA"}

	_, err := ApplySimplePatch(original, patch)
	if err != nil {
		t.Fatalf("ApplySimplePatch() 意外错误: %v", err)
	}
	if original != "alpha beta gamma" {
		t.Fatalf("入参 original 被修改: %q", original)
	}
	if patch.OldText != "beta" || patch.NewText != "BETA" {
		t.Fatalf("入参 patch 被修改: %+v", patch)
	}
}

func TestApplyPatchBatchSequential(t *testing.T) {
	files := map[string]string{
		"a.txt": "hello",
		"b.txt": "one",
	}
	patches := []SimplePatch{
		// 两个补丁都针对 a.txt，顺序求值：第二个补丁必须看到第一个的结果。
		{Path: "a.txt", OldText: "hello", NewText: "hello world"},
		{Path: "a.txt", OldText: "hello world", NewText: "hi world"},
		{Path: "b.txt", OldText: "one", NewText: "two"},
	}

	got, err := ApplyPatchBatch(files, patches)
	if err != nil {
		t.Fatalf("ApplyPatchBatch() 意外错误: %v", err)
	}
	want := map[string]string{
		"a.txt": "hi world",
		"b.txt": "two",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplyPatchBatch() = %#v, 期望 %#v", got, want)
	}
}

func TestApplyPatchBatchAtomicOnFailure(t *testing.T) {
	files := map[string]string{
		"a.txt": "first",
		"b.txt": "second",
	}
	patches := []SimplePatch{
		{Path: "a.txt", OldText: "first", NewText: "FIRST"}, // 本应成功
		{Path: "b.txt", OldText: "missing", NewText: "x"},   // 中途失败
		{Path: "a.txt", OldText: "FIRST", NewText: "AGAIN"}, // 不应被求值
	}

	got, err := ApplyPatchBatch(files, patches)
	if !errors.Is(err, ErrPatchNotUnique) {
		t.Fatalf("错误 = %v, 期望命中 ErrPatchNotUnique", err)
	}
	if got != nil {
		t.Fatalf("失败时返回结果 = %#v, 期望 nil（零部分结果）", got)
	}
	// 入参 map 必须完全未被触碰。
	wantFiles := map[string]string{"a.txt": "first", "b.txt": "second"}
	if !reflect.DeepEqual(files, wantFiles) {
		t.Fatalf("入参 files 被修改: %#v", files)
	}
}

func TestApplyPatchBatchFileNotFound(t *testing.T) {
	files := map[string]string{"a.txt": "x"}
	_, err := ApplyPatchBatch(files, []SimplePatch{{Path: "ghost.txt", OldText: "x", NewText: "y"}})
	if !errors.Is(err, ErrPatchFileNotFound) {
		t.Fatalf("错误 = %v, 期望命中 ErrPatchFileNotFound", err)
	}
}

func TestApplyPatchBatchAddNewFile(t *testing.T) {
	files := map[string]string{"a.txt": "keep"}
	patches := []SimplePatch{
		{Path: "new.txt", OldText: "", NewText: "新增内容", Add: true},
	}

	got, err := ApplyPatchBatch(files, patches)
	if err != nil {
		t.Fatalf("ApplyPatchBatch() 意外错误: %v", err)
	}
	if got["new.txt"] != "新增内容" || got["a.txt"] != "keep" {
		t.Fatalf("ApplyPatchBatch() = %#v", got)
	}
	if _, exists := files["new.txt"]; exists {
		t.Fatalf("入参 files 被修改: %#v", files)
	}
}

func TestApplyPatchBatchDoesNotMutateInput(t *testing.T) {
	files := map[string]string{"a.txt": "before"}
	patches := []SimplePatch{{Path: "a.txt", OldText: "before", NewText: "after"}}

	got, err := ApplyPatchBatch(files, patches)
	if err != nil {
		t.Fatalf("ApplyPatchBatch() 意外错误: %v", err)
	}
	if got["a.txt"] != "after" {
		t.Fatalf("返回结果 = %#v", got)
	}
	if files["a.txt"] != "before" {
		t.Fatalf("入参 files 被修改: %#v", files)
	}
	if len(patches) != 1 || patches[0].OldText != "before" {
		t.Fatalf("入参 patches 被修改: %#v", patches)
	}
}

func TestApplyPatchBatchStable(t *testing.T) {
	files := map[string]string{"a.txt": "x", "b.txt": "y"}
	patches := []SimplePatch{
		{Path: "a.txt", OldText: "x", NewText: "1"},
		{Path: "b.txt", OldText: "y", NewText: "2"},
	}

	first, err := ApplyPatchBatch(files, patches)
	if err != nil {
		t.Fatalf("ApplyPatchBatch() 意外错误: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := ApplyPatchBatch(files, patches)
		if err != nil {
			t.Fatalf("第 %d 次调用意外错误: %v", i, err)
		}
		if !reflect.DeepEqual(again, first) {
			t.Fatalf("第 %d 次调用结果不稳定: %#v != %#v", i, again, first)
		}
	}
}
