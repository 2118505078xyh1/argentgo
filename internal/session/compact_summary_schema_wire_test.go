package session

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// summarySchemaWireDropped 构造一组固定的丢弃条目，用于锁定 =0 关闭时的逐字节基线。
func summarySchemaWireDropped() []Entry {
	return []Entry{
		{Role: "user", Content: "实现词法器"},
		pathEntry("lexer.go"),
		{Role: "tool", Name: "compile", Content: "error: build failed"},
		{Role: "assistant", Content: "Next steps: 补测试"},
	}
}

// summarySchemaWireBaseline 是 summarySchemaWireDropped() 经旧版 Summarize 产出的摘要，
// 逐字锁定。=0 关闭时 Summarize 必须与此完全一致。
const summarySchemaWireBaseline = "## Compaction\n" +
	"- dropped 4 message(s), kept the last 0\n\n" +
	"## Goal\n- 实现词法器\n\n" +
	"## Files changed\n- lexer.go\n\n" +
	"## Failures\n- compile — error: build failed\n\n" +
	"## Next steps\n- Next steps: 补测试\n"

// TestSummarySchemaOffIsByteIdentical 锁定显式 =0 关闭时的逐字节基线。
func TestSummarySchemaOffIsByteIdentical(t *testing.T) {
	for _, value := range []string{"0", " 0 "} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv(summarySchemaEnv, value)
			comp := Summarize(summarySchemaWireDropped(), 0)
			if comp.Summary != summarySchemaWireBaseline {
				t.Errorf("=0 关闭时摘要被改动:\n got %q\nwant %q", comp.Summary, summarySchemaWireBaseline)
			}
			if comp.SummaryEmpty {
				t.Error("=0 关闭时不应计算 SummaryEmpty")
			}
			if strings.Contains(comp.Summary, schemaTitleState) {
				t.Error("=0 关闭时不应补出 State 小节")
			}
		})
	}
}

// TestSummarySchemaDefaultOnFillsMissingSections 验证默认开启时补齐缺失小节：
// 基线的 Goal / Next steps / Files changed 均被识别为已存在，只补 State；
// 已有内容作为前缀逐字保留。
func TestSummarySchemaDefaultOnFillsMissingSections(t *testing.T) {
	// 默认开启：不设置环境变量也应应用 schema。
	t.Setenv(summarySchemaEnv, "")
	comp := Summarize(summarySchemaWireDropped(), 0)
	want := summarySchemaWireBaseline + "\n" + schemaStateSkeleton
	if comp.Summary != want {
		t.Errorf("默认开启时摘要错误:\n got %q\nwant %q", comp.Summary, want)
	}
	if !strings.HasPrefix(comp.Summary, summarySchemaWireBaseline) {
		t.Error("已有摘要内容未作为前缀逐字保留")
	}
	if comp.SummaryEmpty {
		t.Error("含真实内容的摘要不应被标记为空")
	}
	for _, title := range []string{schemaTitleGoal, schemaTitleState, schemaTitleNext, schemaTitleFiles} {
		if !schemaHasSection(comp.Summary, title) {
			t.Errorf("缺失规范小节 %q:\n%s", title, comp.Summary)
		}
	}
}

// TestApplySummarySchemaDetectsEmptySummary 验证空摘要检测：空白/占位符摘要被标记为空，
// 且补全后的骨架（全为 (none)）对 SummaryIsEmpty 仍判定为空，供上层触发重试。
func TestApplySummarySchemaDetectsEmptySummary(t *testing.T) {
	for _, summary := range []string{"", "  \n\t\n", "## Goal\n- (none)\n"} {
		out, empty := ApplySummarySchema(summary, nil)
		if !empty {
			t.Errorf("ApplySummarySchema(%q) 未标记为空", summary)
		}
		if !SummaryIsEmpty(out) {
			t.Errorf("补全后的骨架应仍判定为空: %q", out)
		}
		if want := EnsureSummarySections(summary, nil); out != want {
			t.Errorf("与直接调用原语结果不一致:\n got %q\nwant %q", out, want)
		}
	}
	// 有实质内容时不为空。
	if _, empty := ApplySummarySchema("## Goal\n- 构建词法器\n", nil); empty {
		t.Error("含真实目标的摘要被误判为空")
	}
}

// TestApplySummarySchemaGeneratesFilesFromChangedFiles 验证 Files 段从已记录的改动文件
// 事件生成：空白路径被跳过，Read/Edited 分类正确，已有小节不被改写。
func TestApplySummarySchemaGeneratesFilesFromChangedFiles(t *testing.T) {
	summary := "## Goal\n- 保持原目标\n"
	files := []string{"a.go", "目录/文件.go", "", "b.go"}
	out, empty := ApplySummarySchema(summary, files)
	if empty {
		t.Error("含真实目标的摘要不应被标记为空")
	}
	wantFiles := "## Files\n" +
		"- Read:\n" +
		"  - (none)\n" +
		"- Edited:\n" +
		"  - a.go\n" +
		"  - 目录/文件.go\n" +
		"  - b.go\n"
	if !strings.Contains(out, wantFiles) {
		t.Errorf("Files 段未从改动文件生成:\n got %q\nwant contains %q", out, wantFiles)
	}
	if !strings.HasPrefix(out, summary) {
		t.Error("已有 Goal 段未被逐字保留")
	}
	if !utf8.ValidString(out) {
		t.Error("含 CJK 路径的结果不是合法 UTF-8")
	}
}

// TestApplySummarySchemaDoesNotMutateFiles 验证ApplySummarySchema 不修改 files 入参。
func TestApplySummarySchemaDoesNotMutateFiles(t *testing.T) {
	files := []string{"a.go", "b.go"}
	before := append([]string(nil), files...)
	_, _ = ApplySummarySchema("x", files)
	if !reflect.DeepEqual(files, before) {
		t.Errorf("files 入参被改动: %#v, want %#v", files, before)
	}
}

// TestSummarySchemaOnCJKSafety 验证开启时中文摘要与中文路径安全：
// 结果合法 UTF-8、小节补齐、路径不丢失、中文内容不被误识别为标题。
func TestSummarySchemaOnCJKSafety(t *testing.T) {
	t.Setenv(summarySchemaEnv, "1")
	dropped := []Entry{
		{Role: "user", Content: "实现中文词法器"},
		pathEntry("中文目录/词法器.go"),
		{Role: "assistant", Content: "下一步：补中文注释"},
	}
	comp := Summarize(dropped, 0)
	if !utf8.ValidString(comp.Summary) {
		t.Error("中文摘要不是合法 UTF-8")
	}
	if !strings.Contains(comp.Summary, "中文目录/词法器.go") {
		t.Errorf("中文路径丢失:\n%s", comp.Summary)
	}
	if !strings.Contains(comp.Summary, "实现中文词法器") {
		t.Errorf("中文目标丢失:\n%s", comp.Summary)
	}
	if !schemaHasSection(comp.Summary, schemaTitleState) {
		t.Errorf("未补齐 State 小节:\n%s", comp.Summary)
	}
}

// TestSummarySchemaOnDeterministic 验证开启时输出稳定。
func TestSummarySchemaOnDeterministic(t *testing.T) {
	t.Setenv(summarySchemaEnv, "1")
	want := Summarize(summarySchemaWireDropped(), 0)
	for i := 0; i < 50; i++ {
		if got := Summarize(summarySchemaWireDropped(), 0); !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次结果不稳定:\n got %#v\nwant %#v", i, got, want)
		}
	}
}
