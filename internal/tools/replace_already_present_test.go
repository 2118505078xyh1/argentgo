package tools

import (
	"fmt"
	"strings"
	"testing"
)

// replaceAlreadyPresent 是纯函数，下面用表驱动覆盖精确匹配、空白归一化、
// CJK、空文本、部分匹配与误判防护。
func TestReplaceAlreadyPresent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		newText string
		want    bool
	}{
		{"完全相同", "alpha\nbeta\ngamma\n", "beta", true},
		{"部分匹配", "prefix TARGET suffix\n", "TARGET", true},
		{"空白差异-缩进", "keep\n\tfoo()\n\tbar()\n", "    foo()\n    bar()", true},
		{"空白差异-换行", "alpha beta gamma\n", "alpha\nbeta", true},
		{"空白差异-全角空格", "你好　世界\n", "你好 世界", true},
		{"CJK-完全相同", "中文第一行\n第二行已改\n", "第二行已改", true},
		{"CJK-空白差异", "保留\n  你好  世界\n", "你好 世界", true},
		{"空文本", "任意内容\n", "", false},
		{"纯空白文本", "任意内容\n", " \t\n ", false},
		{"空内容", "", "x", false},
		{"不误判-完全缺席", "alpha beta\n", "completely different", false},
		{"不误判-相似但不同", "alpha beta\n", "alpha beta gamma", false},
		{"不误判-CJK相似但不同", "你好世界\n", "再见世界", false},
		{"不误判-空白折叠后仍不同", "a b c\n", "a b d", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := replaceAlreadyPresent(tt.content, tt.newText); got != tt.want {
				t.Fatalf("replaceAlreadyPresent(%q, %q) = %v, want %v", tt.content, tt.newText, got, tt.want)
			}
		})
	}
}

// alreadyAppliedHint 必须保留工具名前缀与 REPLACE 字样，且带上路径。
func TestAlreadyAppliedHint(t *testing.T) {
	got := alreadyAppliedHint("文档/笔记.txt")
	for _, want := range []string{
		"edit:",
		"REPLACE text is already present in 文档/笔记.txt",
		"may have already been applied",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示 %q 缺少 %q", got, want)
		}
	}
}

// 接线验证：重复编辑时 old 已被移除、new 仍在文件中，诊断必须追加已应用提示，
// 且失败不能改动文件。
func TestEditRepairFailureReportsAlreadyApplied(t *testing.T) {
	root := t.TempDir()
	path := writeFixture(t, root, "f.txt", "header\nALPHA\ntail\n")

	_, err := runEditTool(t, root, "f.txt", "alpha", "ALPHA", true)
	if err == nil {
		t.Fatal("重复编辑应当失败")
	}
	for _, want := range []string{
		"edit repair failed",
		"REPLACE text is already present in f.txt",
		"may have already been applied",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("诊断 %q 缺少 %q", err.Error(), want)
		}
	}
	if got := readFixture(t, path); got != "header\nALPHA\ntail\n" {
		t.Fatalf("失败编辑改动了文件: %q", got)
	}
}

// 反向保护：替换文本确实缺席时不能追加提示，原有诊断断言不受影响。
func TestEditRepairFailureDoesNotReportAlreadyAppliedWhenAbsent(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "f.txt", "alpha beta\nneedle one\nneedle two\n")

	_, err := runEditTool(t, root, "f.txt", "needle wan", "X", true)
	if err == nil {
		t.Fatal("预期修复失败")
	}
	if strings.Contains(err.Error(), "may have already been applied") {
		t.Fatalf("替换文本缺席时不应提示已应用: %q", err)
	}
}

// multi_edit 的接线点在 multi_edit.go（由另一对话负责）。这里演示并验证
// 推荐的追加方式：用 %w 保留原诊断，并以 "; " 拼接提示，不替换原文案。
func TestAlreadyAppliedHintComposesWithMultiEditRepairError(t *testing.T) {
	base := multiEditRepairError("f.txt", 1, 1, 0, "alpha\n", "missing")
	if strings.Contains(base.Error(), "REPLACE text is already present") {
		t.Fatalf("基础诊断不应自带提示: %q", base)
	}
	wrapped := fmt.Errorf("%w; %s", base, alreadyAppliedHint("f.txt"))
	for _, want := range []string{
		"file unchanged (atomic batch)",
		"re-send the whole batch",
		"REPLACE text is already present in f.txt",
	} {
		if !strings.Contains(wrapped.Error(), want) {
			t.Fatalf("组合诊断 %q 缺少 %q", wrapped.Error(), want)
		}
	}
}
