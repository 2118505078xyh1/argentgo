package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// runMultiEditRelocate 使用显式 relocate 标志调用 multi_edit，以便
// 在测试未改动的默认路径的同时测试 opt-in 路径。
func runMultiEditRelocate(t *testing.T, root, rel string, edits []map[string]any, repair, relocate bool) (string, error) {
	t.Helper()
	return runToolArgs(t, root, "multi_edit", map[string]any{
		"path":     rel,
		"edits":    edits,
		"repair":   repair,
		"relocate": relocate,
	})
}

// 当 relocate 缺失（默认）时，拼错的文件名必须产生完全
// 相同的旧错误字符串：没有候选提示、没有额外文本、没有任何
// 行为变化。这逐字节守护 opt-in 的承诺。
func TestMultiEditRelocateDefaultOffIsByteIdentical(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "wrong.txt", "nothing here\n")
	writeFixture(t, root, "right.txt", "apple\nbanana\n")

	_, err := runToolArgs(t, root, "multi_edit", map[string]any{
		"path":  "wrong.txt",
		"edits": []map[string]any{{"old": "banana", "new": "BANANA"}},
	})
	want := "multi_edit: edit 1: old text not found in wrong.txt"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want exactly %q", err, want)
	}
	if strings.Contains(err.Error(), "right.txt") || strings.Contains(err.Error(), "possible filename typo") {
		t.Fatalf("default-off error leaked a relocation hint: %q", err)
	}

	// repair=true 的默认路径同样必须没有提示。
	_, err = runToolArgs(t, root, "multi_edit", map[string]any{
		"path":   "wrong.txt",
		"repair": true,
		"edits":  []map[string]any{{"old": "banana", "new": "BANANA"}},
	})
	if err == nil {
		t.Fatal("expected a repair diagnostic")
	}
	if strings.Contains(err.Error(), "right.txt") || strings.Contains(err.Error(), "possible filename typo") {
		t.Fatalf("repair default-off error leaked a relocation hint: %q", err)
	}
}

// opt-in 重定位会报告 SEARCH 文本实际所在位置，但从不
// 写入该文件：模型必须更正文件名并重新发送。
func TestMultiEditRelocateReportsCandidateWithoutWriting(t *testing.T) {
	root := t.TempDir()
	wrong := writeFixture(t, root, "wrong.txt", "nothing here\n")
	right := writeFixture(t, root, "right.txt", "apple\nbanana\n")

	out, err := runMultiEditRelocate(t, root, "wrong.txt", []map[string]any{
		{"old": "banana", "new": "BANANA"},
	}, false, true)
	if out != "" {
		t.Fatalf("out = %q, want empty on failure", out)
	}
	if err == nil {
		t.Fatal("expected a diagnostic for the mistyped filename")
	}
	msg := err.Error()
	for _, want := range []string{
		"old text not found in wrong.txt",
		"possible filename typo",
		"right.txt",
		"none were modified; correct the path and re-send",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	if got := readFixture(t, right); got != "apple\nbanana\n" {
		t.Fatalf("candidate file was modified: %q", got)
	}
	if got := readFixture(t, wrong); got != "nothing here\n" {
		t.Fatalf("intended file was modified: %q", got)
	}
}

// 当唯一可恢复的匹配是模糊匹配（修复阶梯）时，重定位提示同样有效，
// 并且它被附加到修复诊断中，而不是
// 替换它。
func TestMultiEditRelocateReportsFuzzyCandidateInRepairMode(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "wrong.txt", "nothing here\n")
	writeFixture(t, root, "right.txt", "\tfoo()\n\tbar()\n")

	_, err := runMultiEditRelocate(t, root, "wrong.txt", []map[string]any{
		{"old": "    foo()\n    bar()", "new": "    foo()\n    BAZ()"},
	}, true, true)
	if err == nil {
		t.Fatal("expected a diagnostic for the mistyped filename")
	}
	msg := err.Error()
	for _, want := range []string{
		"multi_edit: repair failed at edit 1 of 1",
		"re-send the whole batch",
		"possible filename typo",
		"right.txt (fuzzy match)",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	if got := readFixture(t, filepath.Join(root, "right.txt")); got != "\tfoo()\n\tbar()\n" {
		t.Fatalf("candidate file was modified: %q", got)
	}
}

// 含 CJK 字符的候选路径必须在遍历、规范
// 路径比较和相对路径格式化后仍为有效 UTF-8。
func TestMultiEditRelocateReportsCJKCandidate(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "文档")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	right := filepath.Join(dir, "正确.txt")
	if err := os.WriteFile(right, []byte("苹果\n香蕉\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "错误.txt", "空\n")

	_, err := runMultiEditRelocate(t, root, "错误.txt", []map[string]any{
		{"old": "香蕉", "new": "香瓜"},
	}, false, true)
	if err == nil {
		t.Fatal("expected a diagnostic for the mistyped CJK filename")
	}
	msg := err.Error()
	for _, want := range []string{
		"old text not found in 错误.txt",
		"文档/正确.txt",
		"none were modified",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
	if !utf8.ValidString(msg) || strings.ContainsRune(msg, utf8.RuneError) {
		t.Fatalf("diagnostic is not valid UTF-8: %q", msg)
	}
	if got := readFixture(t, right); got != "苹果\n香蕉\n" {
		t.Fatalf("CJK candidate file was modified: %q", got)
	}

	// 即使接上了 opt-in 标志，CJK 路径上的默认行为仍保持不变：
	// 正确的文件名仍可正常编辑。
	out, err := runMultiEditRelocate(t, root, "文档/正确.txt", []map[string]any{
		{"old": "香蕉", "new": "香瓜"},
	}, false, true)
	if err != nil {
		t.Fatalf("relocate=true with a correct CJK path: %v", err)
	}
	if want := "edited 文档/正确.txt (1 edits)"; out != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
	if got := readFixture(t, right); got != "苹果\n香瓜\n" {
		t.Fatalf("content = %q", got)
	}
}
