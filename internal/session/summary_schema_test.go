package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// emptyFilesSection 是 fileOps 为空时机械生成的 Files 段。
func emptyFilesSection() string {
	return "## Files\n- Read:\n  - (none)\n- Edited:\n  - (none)\n"
}

func TestEnsureSummarySectionsAddsAllMissing(t *testing.T) {
	got := EnsureSummarySections("", nil)
	want := schemaGoalSkeleton + "\n" +
		schemaStateSkeleton + "\n" +
		schemaNextSkeleton + "\n" +
		emptyFilesSection()
	if got != want {
		t.Errorf("EnsureSummarySections(\"\") =\n%q\nwant\n%q", got, want)
	}
	// 四个小节各出现一次，且顺序固定。
	for _, title := range []string{schemaTitleGoal, schemaTitleState, schemaTitleNext, schemaTitleFiles} {
		if n := strings.Count(got, title); n != 1 {
			t.Errorf("标题 %q 出现 %d 次，want 1", title, n)
		}
	}
	if goal, next, files := strings.Index(got, schemaTitleGoal), strings.Index(got, schemaTitleNext), strings.Index(got, schemaTitleFiles); !(goal < next && next < files) {
		t.Errorf("小节顺序错误: goal=%d next=%d files=%d", goal, next, files)
	}
}

func TestEnsureSummarySectionsKeepsExistingSections(t *testing.T) {
	summary := "## Goal\n- 构建词法器\n\n## Notes\n- 保留我\n\n## Next\n- 跑测试\n"
	got := EnsureSummarySections(summary, nil)
	// Goal / Next 已存在，只补 State 与 Files，且原有内容作为前缀逐字保留。
	want := summary + "\n" + schemaStateSkeleton + "\n" + emptyFilesSection()
	if got != want {
		t.Errorf("EnsureSummarySections() =\n%q\nwant\n%q", got, want)
	}
	if !strings.HasPrefix(got, summary) {
		t.Error("已有小节内容未被原样保留")
	}
	for _, title := range []string{schemaTitleGoal, schemaTitleState, schemaTitleNext, schemaTitleFiles} {
		if n := strings.Count(got, title); n != 1 {
			t.Errorf("标题 %q 出现 %d 次，want 1", title, n)
		}
	}
}

func TestEnsureSummarySectionsRecognizesAliases(t *testing.T) {
	summary := "## 目标\n- 已有目标\n\n## Next steps\n- 已有下一步\n\n## Files changed\n- a.go\n"
	got := EnsureSummarySections(summary, nil)
	// 别名均应视为小节已存在：只补 State。
	want := summary + "\n" + schemaStateSkeleton
	if got != want {
		t.Errorf("EnsureSummarySections() =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "\n## Goal\n") || strings.Contains(got, "\n## Next\n") || strings.Contains(got, "\n## Files\n") {
		t.Error("别名字节导致补出重复的小节")
	}
}

func TestEnsureSummarySectionsGeneratesFilesFromOps(t *testing.T) {
	ops := []FileOp{
		{Path: "a.go", Edited: false, Lines: "10-20"},
		{Path: "b.go", Edited: true, Lines: "go test ./..."},
		{Path: "c.go", Edited: false},
		{Path: "d.go", Edited: true, Lines: "5-9"},
	}
	got := EnsureSummarySections("", ops)
	files := "## Files\n" +
		"- Read:\n" +
		"  - a.go: 10-20\n" +
		"  - c.go\n" +
		"- Edited:\n" +
		"  - b.go: go test ./...\n" +
		"  - d.go: 5-9\n"
	want := schemaGoalSkeleton + "\n" +
		schemaStateSkeleton + "\n" +
		schemaNextSkeleton + "\n" +
		files
	if got != want {
		t.Errorf("Files 段生成错误:\ngot  =\n%q\nwant =\n%q", got, want)
	}
}

func TestEnsureSummarySectionsFilesIgnoreOpsWhenSectionExists(t *testing.T) {
	summary := "## Files\n- 原有内容保持不动\n"
	ops := []FileOp{{Path: "new.go", Edited: true, Lines: "1-3"}}
	got := EnsureSummarySections(summary, ops)
	want := summary + "\n" +
		schemaGoalSkeleton + "\n" +
		schemaStateSkeleton + "\n" +
		schemaNextSkeleton
	if got != want {
		t.Errorf("已有 Files 段不应被 fileOps 改写:\ngot  =\n%q\nwant =\n%q", got, want)
	}
	if !strings.HasPrefix(got, summary) || strings.Contains(got, "new.go") {
		t.Error("已有 Files 段内容被修改")
	}
}

func TestEnsureSummarySectionsTruncatesLines(t *testing.T) {
	t.Run("ascii command", func(t *testing.T) {
		ops := []FileOp{{Path: "run.sh", Edited: false, Lines: strings.Repeat("x", 120)}}
		got := EnsureSummarySections("", ops)
		entry := "  - run.sh: " + strings.Repeat("x", schemaLineLimit) + "\n"
		if !strings.Contains(got, entry) {
			t.Errorf("长命令未按 %d 符文裁剪: %q", schemaLineLimit, got)
		}
		if strings.Contains(got, strings.Repeat("x", schemaLineLimit+1)) {
			t.Error("裁剪后仍超过上限")
		}
	})

	t.Run("cjk lines", func(t *testing.T) {
		lines := strings.Repeat("汉", 150)
		ops := []FileOp{{Path: "文件.go", Edited: true, Lines: lines}}
		got := EnsureSummarySections("", ops)
		entry := "  - 文件.go: " + strings.Repeat("汉", schemaLineLimit) + "\n"
		if !strings.Contains(got, entry) {
			t.Errorf("CJK 行未按 %d 符文裁剪: %q", schemaLineLimit, got)
		}
		if !utf8.ValidString(got) {
			t.Error("裁剪后的摘要不是合法 UTF-8")
		}
	})
}

func TestEnsureSummarySectionsCJKAndSpecialPaths(t *testing.T) {
	ops := []FileOp{
		{Path: "目录/文件 名.go", Edited: false, Lines: "第 1-10 行"},
		// 路径内嵌换行，尝试注入伪造的标题行；应被折叠为单行，不能产生真正的 "##"。
		{Path: "evil.go\n## Injected", Edited: true, Lines: "命令\n## Fake"},
	}
	got := EnsureSummarySections("## Goal\n- 已有中文目标\n", ops)
	if !utf8.ValidString(got) {
		t.Error("含 CJK 的结果不是合法 UTF-8")
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "## Injected") || strings.HasPrefix(line, "## Fake") {
			t.Errorf("路径/行文本注入了伪造标题: %q", line)
		}
	}
	if !strings.Contains(got, "  - 目录/文件 名.go: 第 1-10 行\n") {
		t.Errorf("CJK 路径或行丢失: %q", got)
	}
	if !strings.Contains(got, "  - evil.go ## Injected: 命令 ## Fake\n") {
		t.Errorf("特殊路径未正确折叠为单行: %q", got)
	}
}

func TestEnsureSummarySectionsDoesNotMutateInputs(t *testing.T) {
	summary := "## Goal\n- 原始内容\n"
	ops := []FileOp{
		{Path: "a.go", Edited: false, Lines: "10-20"},
		{Path: "b.go", Edited: true, Lines: "go test ./..."},
	}
	summaryCopy := summary
	opsCopy := append([]FileOp(nil), ops...)

	_ = EnsureSummarySections(summary, ops)

	if summary != summaryCopy {
		t.Errorf("summary 入参被改动: %q", summary)
	}
	if !reflect.DeepEqual(ops, opsCopy) {
		t.Errorf("fileOps 入参被改动: %#v, want %#v", ops, opsCopy)
	}
}

func TestEnsureSummarySectionsDeterministic(t *testing.T) {
	summary := "## Notes\n- 保持\n"
	ops := []FileOp{
		{Path: "a.go", Edited: false, Lines: "1-2"},
		{Path: "b.go", Edited: true, Lines: "3-4"},
	}
	want := EnsureSummarySections(summary, ops)
	for i := 0; i < 50; i++ {
		if got := EnsureSummarySections(summary, ops); got != want {
			t.Fatalf("第 %d 次调用结果不稳定:\ngot  = %q\nwant = %q", i, got, want)
		}
	}
}

func TestSummaryIsEmpty(t *testing.T) {
	generated := EnsureSummarySections("", nil)
	tests := []struct {
		name    string
		summary string
		want    bool
	}{
		{name: "empty string", summary: "", want: true},
		{name: "whitespace only", summary: "  \n\t\n ", want: true},
		{name: "headings only", summary: "## Goal\n## Files\n", want: true},
		{name: "none placeholders", summary: "## Goal\n- (none)\n## Files\n- Read: (none)\n", want: true},
		{name: "generated skeleton", summary: generated, want: true},
		{name: "real goal content", summary: "## Goal\n- 构建词法器\n", want: false},
		{name: "plain content", summary: "still working on parser", want: false},
		{name: "label with real value", summary: "## State\n- Done: 已完成\n", want: false},
		{name: "empty label with content elsewhere", summary: "## Files\n- Read:\n- Edited: a.go\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SummaryIsEmpty(tt.summary); got != tt.want {
				t.Errorf("SummaryIsEmpty(%q) = %v, want %v", tt.summary, got, tt.want)
			}
		})
	}
}
