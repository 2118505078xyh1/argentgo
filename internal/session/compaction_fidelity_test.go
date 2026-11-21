package session

import (
	"reflect"
	"strings"
	"testing"
)

// fullSummary 构造四要素齐全的摘要，与 Summarize 的输出格式一致。
func fullSummary() string {
	return "## Compaction\n" +
		"- dropped 4 message(s), kept the last 0\n\n" +
		"## Goal\n- build lexer\n\n" +
		"## Files changed\n- lexer.go\n- parser.go\n\n" +
		"## Failures\n- compile — error: undefined: token\n\n" +
		"## Next steps\n- add tests\n- run vet\n"
}

func TestMeasureCompactionFidelityAllElements(t *testing.T) {
	f := MeasureCompactionFidelity(fullSummary())
	if !f.HasGoal {
		t.Error("HasGoal = false, want true")
	}
	if f.ChangedFiles != 2 {
		t.Errorf("ChangedFiles = %d, want 2", f.ChangedFiles)
	}
	if f.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1", f.FailureCount)
	}
	if f.NextSteps != 2 {
		t.Errorf("NextSteps = %d, want 2", f.NextSteps)
	}
	if f.Score != 1 {
		t.Errorf("Score = %v, want 1", f.Score)
	}
	if !f.Complete {
		t.Error("Complete = false, want true")
	}
}

func TestMeasureCompactionFidelityMissingElements(t *testing.T) {
	tests := []struct {
		name string
		// 每个构造函数从齐全摘要中删掉一个章节。
		drop         string
		wantGoal     bool
		wantFiles    int
		wantFailures int
		wantNext     int
		wantScore    float64
	}{
		{name: "no goal", drop: "## Goal\n- build lexer\n\n", wantGoal: false, wantFiles: 2, wantFailures: 1, wantNext: 2, wantScore: 0.75},
		{name: "no files", drop: "## Files changed\n- lexer.go\n- parser.go\n\n", wantGoal: true, wantFiles: 0, wantFailures: 1, wantNext: 2, wantScore: 0.75},
		{name: "no failures", drop: "## Failures\n- compile — error: undefined: token\n\n", wantGoal: true, wantFiles: 2, wantFailures: 0, wantNext: 2, wantScore: 0.75},
		{name: "no next steps", drop: "## Next steps\n- add tests\n- run vet\n", wantGoal: true, wantFiles: 2, wantFailures: 1, wantNext: 0, wantScore: 0.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := MeasureCompactionFidelity(strings.Replace(fullSummary(), tt.drop, "", 1))
			if f.HasGoal != tt.wantGoal || f.ChangedFiles != tt.wantFiles ||
				f.FailureCount != tt.wantFailures || f.NextSteps != tt.wantNext {
				t.Errorf("fidelity = %+v, want goal=%v files=%d failures=%d next=%d",
					f, tt.wantGoal, tt.wantFiles, tt.wantFailures, tt.wantNext)
			}
			if f.Score != tt.wantScore {
				t.Errorf("Score = %v, want %v", f.Score, tt.wantScore)
			}
			if f.Complete {
				t.Error("Complete = true, want false when an element is missing")
			}
		})
	}
}

func TestMeasureCompactionFidelityScoreBoundaries(t *testing.T) {
	// 逐段累加四要素，Score 应依次为 0、0.25、0.5、0.75、1。
	parts := []string{
		"## Goal\n- goal\n",
		"## Files changed\n- a.go\n",
		"## Failures\n- error\n",
		"## Next steps\n- next\n",
	}
	wantScores := []float64{0, 0.25, 0.5, 0.75, 1}
	var summary string
	for i, want := range wantScores {
		f := MeasureCompactionFidelity(summary)
		if f.Score != want {
			t.Errorf("after %d elements: Score = %v, want %v", i, f.Score, want)
		}
		if f.Complete != (i == 4) {
			t.Errorf("after %d elements: Complete = %v, want %v", i, f.Complete, i == 4)
		}
		if i < len(parts) {
			summary += parts[i] + "\n"
		}
	}
}

func TestMeasureCompactionFidelityEmpty(t *testing.T) {
	for _, summary := range []string{"", "no sections here", "## Compaction\n- dropped 1 message(s), kept the last 0\n"} {
		f := MeasureCompactionFidelity(summary)
		if f.HasGoal || f.ChangedFiles != 0 || f.FailureCount != 0 || f.NextSteps != 0 ||
			f.Score != 0 || f.Complete {
			t.Errorf("MeasureCompactionFidelity(%q) = %+v, want all zero", summary, f)
		}
	}
}

func TestMeasureCompactionFidelityCJK(t *testing.T) {
	summary := "## Compaction\n- 已丢弃 3 条消息\n\n" +
		"## 目标\n- 实现词法分析器，支持 Unicode 标识符\n\n" +
		"## 改动文件\n- 中文目录/词法分析器.go\n- parser.go\n\n" +
		"## 失败\n- 编译 — 失败: 未定义的符号 token\n\n" +
		"## 下一步\n- 补中文注释并跑测试\n"
	f := MeasureCompactionFidelity(summary)
	want := CompactionFidelity{
		HasGoal: true, ChangedFiles: 2, FailureCount: 1, NextSteps: 1,
		Score: 1, Complete: true,
	}
	if f != want {
		t.Errorf("CJK fidelity = %+v, want %+v", f, want)
	}
}

func TestMeasureCompactionFidelityIgnoresPlaceholdersAndPromotedHeaders(t *testing.T) {
	// 空章节占位符与重复压缩产生的误提升标题都不应被计入要素。
	summary := "## Compaction\n- dropped 2 message(s), kept the last 1\n\n" +
		"## Goal\n- (none)\n\n" +
		"## Files changed\n- (none)\n\n" +
		"## Failures\n- (none)\n\n" +
		"## Next steps\n- ## Next steps\n- (none)\n"
	f := MeasureCompactionFidelity(summary)
	if f.HasGoal || f.ChangedFiles != 0 || f.FailureCount != 0 || f.NextSteps != 0 ||
		f.Score != 0 || f.Complete {
		t.Errorf("fidelity = %+v, want all zero", f)
	}
}

func TestMeasureCompactionFidelityDoesNotModifyInput(t *testing.T) {
	summary := fullSummary()
	before := summary
	_ = MeasureCompactionFidelity(summary)
	if summary != before {
		t.Errorf("input was modified: got %q, want %q", summary, before)
	}
}

func TestMeasureCompactionFidelityStable(t *testing.T) {
	// 同一摘要多次度量必须得到完全相同的结果（确定性/稳定性）。
	summary := fullSummary()
	want := MeasureCompactionFidelity(summary)
	for i := 0; i < 100; i++ {
		if got := MeasureCompactionFidelity(summary); got != want {
			t.Fatalf("run %d: fidelity = %+v, want %+v", i, got, want)
		}
	}
}

func TestMeasureCompactionFidelityOnSummarizeOutput(t *testing.T) {
	// 直接对 Summarize 的真实输出做度量，确保解析器与生产格式对齐。
	dropped := []Entry{
		{Role: "user", Content: "实现词法分析器"},
		pathEntry("lexer.go"),
		{Role: "tool", Name: "compile", Content: "error: build failed"},
		{Role: "assistant", Content: "Next steps: 补测试"},
	}
	comp := Summarize(dropped, 0)
	f := MeasureCompactionFidelity(comp.Summary)
	if !f.HasGoal || f.ChangedFiles != 1 || f.FailureCount != 1 || f.NextSteps != 1 ||
		f.Score != 1 || !f.Complete {
		t.Errorf("fidelity of Summarize output = %+v, want all four elements\nsummary:\n%s", f, comp.Summary)
	}
}

func TestIsCompactionLossyMinScore(t *testing.T) {
	// Score 依次为 0、0.75、1，分别用不同 minScore 检查严格小于才判有损。
	zero := ""
	threeOfFour := strings.Replace(fullSummary(), "## Goal\n- build lexer\n\n", "", 1)
	full := fullSummary()

	tests := []struct {
		name     string
		summary  string
		minScore float64
		want     bool
	}{
		{name: "score 0 with min 0.75", summary: zero, minScore: 0.75, want: true},
		{name: "score 0.75 equals min", summary: threeOfFour, minScore: 0.75, want: false},
		{name: "score 0.75 above min 0.5", summary: threeOfFour, minScore: 0.5, want: false},
		{name: "score 0.75 below min 1", summary: threeOfFour, minScore: 1, want: true},
		{name: "score 1 equals min 1", summary: full, minScore: 1, want: false},
		{name: "score 1 with min 0.5", summary: full, minScore: 0.5, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCompactionLossy(tt.summary, tt.minScore); got != tt.want {
				t.Errorf("IsCompactionLossy(score=%v, min=%v) = %v, want %v",
					MeasureCompactionFidelity(tt.summary).Score, tt.minScore, got, tt.want)
			}
		})
	}
}

func TestIsCompactionLossyDefaultMinScore(t *testing.T) {
	// minScore<=0 一律使用默认阈值 0.75：0.5 有损，0.75 与 1 无损。
	half := "## Goal\n- goal\n\n## Files changed\n- a.go\n"
	threeOfFour := strings.Replace(fullSummary(), "## Goal\n- build lexer\n\n", "", 1)
	full := fullSummary()

	for _, minScore := range []float64{0, -0.5, -1} {
		if !IsCompactionLossy(half, minScore) {
			t.Errorf("score 0.5 with default min (minScore=%v) should be lossy", minScore)
		}
		if IsCompactionLossy(threeOfFour, minScore) {
			t.Errorf("score 0.75 equals default min (minScore=%v), should not be lossy", minScore)
		}
		if IsCompactionLossy(full, minScore) {
			t.Errorf("score 1 with default min (minScore=%v), should not be lossy", minScore)
		}
	}
}

func TestCompactionFidelityStructShape(t *testing.T) {
	// 防止字段集合在后续接线时被意外改动：这里锁定类型结构。
	f := CompactionFidelity{}
	if !reflect.DeepEqual(f, CompactionFidelity{
		HasGoal: false, ChangedFiles: 0, FailureCount: 0,
		NextSteps: 0, Score: 0, Complete: false,
	}) {
		t.Errorf("unexpected zero value: %+v", f)
	}
}
