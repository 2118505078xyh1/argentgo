package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func sampleContextInput() ContextInput {
	return ContextInput{
		MemoryDocs: []MemoryDoc{
			{Path: "/repo/AGENTS.md", Bytes: 8000, Truncated: true, Reason: "per-file limit"},
			{Path: "/repo/CLAUDE.md", Bytes: 1200},
		},
		SkillCount:   3,
		CommandCount: 7,
		Tools:        []string{"shell", "read_file"},
		SandboxMode:  "workspace-write",
		ApprovalMode: "on-request",
		Ignore: IgnoreStatus{
			Files: []string{"/repo/.argentignore", "/repo/pkg/.argentignore"},
			Rules: 5,
		},
	}
}

// 每个有文档记录的章节都会出现，包括逐条文档的截断原因。
func TestBuildContextReportContainsAllSections(t *testing.T) {
	report := BuildContextReport(sampleContextInput())
	for _, section := range []string{
		"context:", "memory docs", "skills:", "commands:", "tools",
		"sandbox:", "approval:", "ignore files", "rules:", "estimated bytes:",
	} {
		if !strings.Contains(report, section) {
			t.Errorf("report missing section %q:\n%s", section, report)
		}
	}
	for _, detail := range []string{
		"/repo/AGENTS.md", "per-file limit", "8000 B", "skills: 3",
		"commands: 7", "read_file", "workspace-write", "on-request",
		"/repo/.argentignore", "rules: 5", "estimated bytes: 9200",
	} {
		if !strings.Contains(report, detail) {
			t.Errorf("report missing detail %q:\n%s", detail, report)
		}
	}
}

// JSON 合法，携带相同的规范化值和稳定的键。
func TestBuildContextReportJSONValid(t *testing.T) {
	data, err := BuildContextReportJSON(sampleContextInput())
	if err != nil {
		t.Fatalf("BuildContextReportJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	for _, key := range []string{
		"memory_docs", "skill_count", "command_count", "tools",
		"sandbox_mode", "approval_mode", "ignore_files", "ignore_rules",
		"estimated_bytes",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("JSON missing key %q: %s", key, data)
		}
	}
	if got := decoded["estimated_bytes"]; got != float64(9200) {
		t.Errorf("estimated_bytes = %v, want 9200", got)
	}
	if got := decoded["sandbox_mode"]; got != "workspace-write" {
		t.Errorf("sandbox_mode = %v", got)
	}
}

// 空输入仍能渲染所有章节而不会崩溃。
func TestBuildContextReportEmptyInput(t *testing.T) {
	report := BuildContextReport(ContextInput{})
	for _, want := range []string{
		"memory docs: none", "tools: none", "ignore files: none",
		"skills: 0", "commands: 0", "rules: 0", "estimated bytes: 0",
		"sandbox: unspecified", "approval: unspecified",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("empty report missing %q:\n%s", want, report)
		}
	}
	if _, err := BuildContextReportJSON(ContextInput{}); err != nil {
		t.Fatalf("JSON on empty input: %v", err)
	}
}

// 病态输入无法撑大报告：在文本和 JSON 两个视图中，列表都有上限，名称
// 均被裁剪。
func TestBuildContextReportBoundedOutput(t *testing.T) {
	in := ContextInput{Tools: make([]string, 0, 200)}
	huge := strings.Repeat("Z", 1000)
	for i := 0; i < 200; i++ {
		in.Tools = append(in.Tools, fmt.Sprintf("%s-%d", huge, i))
		in.MemoryDocs = append(in.MemoryDocs, MemoryDoc{
			Path: fmt.Sprintf("%s/%d", huge, i), Bytes: 1_000_000,
		})
		in.Ignore.Files = append(in.Ignore.Files, fmt.Sprintf("%s/%d", huge, i))
	}

	report := BuildContextReport(in)
	if len(report) > 50_000 {
		t.Fatalf("report length %d exceeds bound", len(report))
	}
	if strings.Contains(report, huge) {
		t.Error("report contains an unclipped 1000-byte name")
	}
	if n := strings.Count(report, "  - "); n > maxContextItems {
		t.Errorf("report lists %d memory docs, cap is %d", n, maxContextItems)
	}
	data, err := BuildContextReportJSON(in)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if len(data) > 50_000 || strings.Contains(string(data), huge) {
		t.Errorf("JSON report is not bounded (len=%d)", len(data))
	}
}

// 排序和裁剪使报告具有确定性，即使调用方的
// 切片以不同顺序到达也是如此。
func TestBuildContextReportDeterministic(t *testing.T) {
	a := ContextInput{
		Tools:      []string{"shell", "read_file", "patch"},
		MemoryDocs: []MemoryDoc{{Path: "b.md", Bytes: 2}, {Path: "a.md", Bytes: 1}},
		Ignore:     IgnoreStatus{Files: []string{"z", "a"}, Rules: 2},
	}
	b := ContextInput{
		Tools:      []string{"patch", "shell", "read_file"},
		MemoryDocs: []MemoryDoc{{Path: "a.md", Bytes: 1}, {Path: "b.md", Bytes: 2}},
		Ignore:     IgnoreStatus{Files: []string{"z", "a"}, Rules: 2},
	}
	if got, want := BuildContextReport(a), BuildContextReport(b); got != want {
		t.Errorf("reports differ:\n%s\n---\n%s", got, want)
	}
	if got, want := BuildContextReport(a), BuildContextReport(a); got != want {
		t.Error("repeated calls are not identical")
	}
	ja, _ := BuildContextReportJSON(a)
	jb, _ := BuildContextReportJSON(b)
	if string(ja) != string(jb) {
		t.Error("JSON reports differ for equivalent inputs")
	}
}

// 内置描述符和钩子遵循 /review 的注册模式。
func TestContextCommandAndHook(t *testing.T) {
	cmd := ContextCommand()
	if cmd.Name != "context" || !strings.Contains(strings.ToLower(cmd.Description), "read-only") {
		t.Fatalf("unexpected descriptor: %+v", cmd)
	}
	set := Discover(nil)
	set.Add(cmd)
	if got, ok := set.Get("context"); !ok || got.Path != "<builtin:context>" {
		t.Fatalf("registration failed: %+v, ok=%v", got, ok)
	}

	in := sampleContextInput()
	text, err := ContextHook{Input: in}.Run("ignored arguments")
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	if text != BuildContextReport(in) {
		t.Error("hook output differs from BuildContextReport")
	}
}
