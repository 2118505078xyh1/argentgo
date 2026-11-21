package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// rawSummary 模拟 Summarize 的原始输出：Goal 有真实内容，且末尾带工具活动段。
func rawSummaryWithSections() string {
	return "## Compaction\n" +
		"- dropped 6 message(s), kept the last 2\n\n" +
		"## Goal\n- 构建词法分析器\n- 修复解析错误\n\n" +
		"## Files changed\n- lexer.go\n- parser.go\n\n" +
		"## Failures\n- compile — error: undefined: token\n\n" +
		"## Next steps\n- 补充单元测试\n- 运行 go vet\n\n" +
		"## Tool activity\n- read lexer.go:10-20\n- edit parser.go\n- bash go test ./...\n"
}

const (
	wantGoalBlock     = "## Goal\n- 构建词法分析器\n- 修复解析错误\n"
	wantActivityBlock = "## Tool activity\n- read lexer.go:10-20\n- edit parser.go\n- bash go test ./...\n"
)

// rebuiltSummary 模拟 buildCompactionSummary 的输出：Goal 硬编码为 (none)，且没有工具活动段。
func rebuiltSummaryWithoutSections() string {
	return "## Compaction\n" +
		"- dropped 6 message(s), kept the last 2\n\n" +
		"## Goal\n- (none)\n\n" +
		"## Files changed\n- lexer.go\n- parser.go\n\n" +
		"## Failures\n- (none)\n\n" +
		"## Next steps\n- (none)\n"
}

func TestParseSummarySectionsBoth(t *testing.T) {
	got := ParseSummarySections(rawSummaryWithSections())
	want := SummarySections{
		Goal:         wantGoalBlock,
		ToolActivity: wantActivityBlock,
		HasGoal:      true,
		HasActivity:  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseSummarySections() = %#v, want %#v", got, want)
	}
}

func TestParseSummarySectionsMissingSections(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		want    SummarySections
	}{
		{
			name:    "only goal",
			summary: "## Compaction\n- x\n\n## Goal\n- 目标\n\n## Files changed\n- a.go\n",
			want:    SummarySections{Goal: "## Goal\n- 目标\n", HasGoal: true},
		},
		{
			name:    "only tool activity",
			summary: "## Compaction\n- x\n\n## Tool activity\n- read a.go\n",
			want:    SummarySections{ToolActivity: "## Tool activity\n- read a.go\n", HasActivity: true},
		},
		{
			name:    "neither section",
			summary: "## Compaction\n- x\n\n## Files changed\n- a.go\n",
			want:    SummarySections{},
		},
		{
			name:    "empty summary",
			summary: "",
			want:    SummarySections{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseSummarySections(tt.summary); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseSummarySections() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseSummarySectionsCJKContent(t *testing.T) {
	summary := "## 目标\n- 实现中文摘要\n- 保持原样\n\n" +
		"## 工具活动\n- 读取 文件.go:3-5\n- 编辑 文件.go\n"
	got := ParseSummarySections(summary)
	want := SummarySections{
		Goal:         "## 目标\n- 实现中文摘要\n- 保持原样\n",
		ToolActivity: "## 工具活动\n- 读取 文件.go:3-5\n- 编辑 文件.go\n",
		HasGoal:      true,
		HasActivity:  true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseSummarySections() = %#v, want %#v", got, want)
	}
	if !utf8.ValidString(got.Goal) || !utf8.ValidString(got.ToolActivity) {
		t.Error("CJK 内容不是合法的 UTF-8，可能被按字节截断")
	}
	if !strings.Contains(got.Goal, "实现中文摘要") || !strings.Contains(got.ToolActivity, "读取 文件.go:3-5") {
		t.Errorf("CJK 内容丢失: goal=%q, activity=%q", got.Goal, got.ToolActivity)
	}
}

func TestParseSummarySectionsBoundaries(t *testing.T) {
	t.Run("aliases and trailing colon", func(t *testing.T) {
		got := ParseSummarySections("## Goals:\n- g\n\n## TOOL ACTIVITIES\n- a\n")
		if !got.HasGoal || got.Goal != "## Goals:\n- g\n" {
			t.Errorf("Goal = %q, HasGoal = %v", got.Goal, got.HasGoal)
		}
		if !got.HasActivity || got.ToolActivity != "## TOOL ACTIVITIES\n- a\n" {
			t.Errorf("ToolActivity = %q, HasActivity = %v", got.ToolActivity, got.HasActivity)
		}
	})

	t.Run("bullet containing heading text is not a boundary", func(t *testing.T) {
		got := ParseSummarySections("## Goal\n- 注意 ## Tool activity 只是文本\n- 真实目标\n\n## Files changed\n- a.go\n")
		if !got.HasGoal || got.HasActivity {
			t.Fatalf("HasGoal = %v, HasActivity = %v, want true/false", got.HasGoal, got.HasActivity)
		}
		if want := "## Goal\n- 注意 ## Tool activity 只是文本\n- 真实目标\n"; got.Goal != want {
			t.Errorf("Goal = %q, want %q", got.Goal, want)
		}
	})

	t.Run("duplicate headings keep the first section", func(t *testing.T) {
		got := ParseSummarySections("## Goal\n- first\n\n## Goal\n- second\n\n## Tool activity\n- act\n")
		if !got.HasGoal || got.Goal != "## Goal\n- first\n" {
			t.Errorf("Goal = %q, HasGoal = %v, want first section", got.Goal, got.HasGoal)
		}
	})

	t.Run("empty section is still present", func(t *testing.T) {
		got := ParseSummarySections("## Goal\n## Tool activity\n- act\n")
		if !got.HasGoal || got.Goal != "## Goal\n" {
			t.Errorf("Goal = %q, HasGoal = %v", got.Goal, got.HasGoal)
		}
		if !got.HasActivity || got.ToolActivity != "## Tool activity\n- act\n" {
			t.Errorf("ToolActivity = %q, HasActivity = %v", got.ToolActivity, got.HasActivity)
		}
	})
}

func TestPreserveSectionsReplacesGoalAndAppendsActivity(t *testing.T) {
	got := PreserveSections(rebuiltSummaryWithoutSections(), rawSummaryWithSections())
	want := "## Compaction\n" +
		"- dropped 6 message(s), kept the last 2\n\n" +
		wantGoalBlock + "\n" +
		"## Files changed\n- lexer.go\n- parser.go\n\n" +
		"## Failures\n- (none)\n\n" +
		"## Next steps\n- (none)\n\n" +
		wantActivityBlock
	if got != want {
		t.Errorf("PreserveSections() =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "- (none)\n\n## Files changed") {
		t.Error("Goal 段未被 orig 内容替换")
	}
}

func TestPreserveSectionsOnlyGoal(t *testing.T) {
	orig := "## Goal\n- 唯一目标\n"
	got := PreserveSections(rebuiltSummaryWithoutSections(), orig)
	want := "## Compaction\n- dropped 6 message(s), kept the last 2\n\n" +
		"## Goal\n- 唯一目标\n\n" +
		"## Files changed\n- lexer.go\n- parser.go\n\n" +
		"## Failures\n- (none)\n\n" +
		"## Next steps\n- (none)\n"
	if got != want {
		t.Errorf("PreserveSections() =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "## Tool activity") {
		t.Error("orig 无工具活动段时不应追加该段")
	}
}

func TestPreserveSectionsOnlyActivity(t *testing.T) {
	orig := "## Tool activity\n- read only.go\n"
	rebuilt := rebuiltSummaryWithoutSections()
	got := PreserveSections(rebuilt, orig)
	want := rebuilt + "\n## Tool activity\n- read only.go\n"
	if got != want {
		t.Errorf("PreserveSections() =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(got, "## Goal\n- (none)\n") {
		t.Error("orig 无 Goal 段时不应改动 rebuilt 的 Goal 段")
	}
}

func TestPreserveSectionsWithoutSectionsReturnsRebuilt(t *testing.T) {
	rebuilt := rebuiltSummaryWithoutSections()
	tests := []struct {
		name string
		orig string
	}{
		{name: "empty", orig: ""},
		{name: "compaction only", orig: "## Compaction\n- x\n"},
		{name: "other sections", orig: "## Files changed\n- a.go\n\n## Failures\n- err\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PreserveSections(rebuilt, tt.orig); got != rebuilt {
				t.Errorf("PreserveSections() = %q, want rebuilt 原样返回", got)
			}
		})
	}
}

func TestPreserveSectionsFixedOrder(t *testing.T) {
	// rebuilt 没有 Goal 标题，两段都需要追加；无论 orig 中顺序如何，输出恒为 Goal 在前。
	rebuilt := "## Compaction\n- x\n"
	orig := "## Tool activity\n- act one\n\n## Goal\n- 目标一\n"
	got := PreserveSections(rebuilt, orig)
	want := "## Compaction\n- x\n\n## Goal\n- 目标一\n\n## Tool activity\n- act one\n"
	if got != want {
		t.Errorf("PreserveSections() =\n%q\nwant\n%q", got, want)
	}
	goalIdx := strings.Index(got, "## Goal")
	activityIdx := strings.Index(got, "## Tool activity")
	if goalIdx < 0 || activityIdx < 0 || goalIdx >= activityIdx {
		t.Errorf("固定顺序被破坏: goalIdx=%d, activityIdx=%d", goalIdx, activityIdx)
	}
}

func TestPreserveSectionsIdempotent(t *testing.T) {
	tests := []struct {
		name    string
		rebuilt string
		orig    string
	}{
		{
			name:    "both sections",
			rebuilt: rebuiltSummaryWithoutSections(),
			orig:    rawSummaryWithSections(),
		},
		{
			name:    "only goal",
			rebuilt: rebuiltSummaryWithoutSections(),
			orig:    "## Goal\n- 唯一目标\n",
		},
		{
			name:    "only activity",
			rebuilt: rebuiltSummaryWithoutSections(),
			orig:    "## Tool activity\n- read a.go\n",
		},
		{
			name:    "reversed order in orig",
			rebuilt: "## Compaction\n- x\n",
			orig:    "## Tool activity\n- a\n\n## Goal\n- g\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			once := PreserveSections(tt.rebuilt, tt.orig)
			twice := PreserveSections(once, tt.orig)
			if once != twice {
				t.Errorf("非幂等:\nonce =\n%q\ntwice =\n%q", once, twice)
			}
		})
	}
}

func TestPreserveSectionsDoesNotMutateInputs(t *testing.T) {
	rebuilt := rebuiltSummaryWithoutSections()
	orig := rawSummaryWithSections()
	rebuiltCopy := rebuilt
	origCopy := orig

	_ = ParseSummarySections(orig)
	_ = PreserveSections(rebuilt, orig)

	if rebuilt != rebuiltCopy || orig != origCopy {
		t.Errorf("入参被改动: rebuilt=%q, orig=%q", rebuilt, orig)
	}
}

func TestPreserveSectionsDeterministic(t *testing.T) {
	rebuilt := rebuiltSummaryWithoutSections()
	orig := rawSummaryWithSections()
	want := PreserveSections(rebuilt, orig)
	for i := 0; i < 50; i++ {
		if got := PreserveSections(rebuilt, orig); got != want {
			t.Fatalf("第 %d 次调用结果不稳定", i)
		}
	}
}
