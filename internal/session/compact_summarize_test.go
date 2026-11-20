package session

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// pathEntry builds a tool entry whose payload carries a file path, matching
// the "path" key that pathsFromData collects.
func pathEntry(path string) Entry {
	data, _ := json.Marshal(map[string]any{"path": path})
	return Entry{Role: "tool", Name: "edit", Data: data}
}

func TestSummarizeKeepsNextStepsOnNonFirstLine(t *testing.T) {
	dropped := []Entry{
		{Role: "assistant", Content: "已改完 X。\n\nNext steps: 加 lexer 并补测试"},
	}
	got := Summarize(dropped, 0)
	if len(got.NextSteps) != 1 {
		t.Fatalf("NextSteps = %q, want exactly one entry", got.NextSteps)
	}
	if !strings.Contains(got.NextSteps[0], "加 lexer 并补测试") {
		t.Fatalf("NextSteps = %q, want the real next-step line", got.NextSteps)
	}
	if strings.Contains(got.NextSteps[0], "已改完") {
		t.Fatalf("next step was taken from the first line: %q", got.NextSteps)
	}
	if !strings.Contains(got.Summary, "加 lexer 并补测试") {
		t.Fatalf("summary dropped next steps:\n%s", got.Summary)
	}
}

func TestSummarizeKeepsFailureOnNonFirstLine(t *testing.T) {
	dropped := []Entry{
		{Role: "tool", Name: "shell", Content: "compiling...\nbuilding\nerror: undefined: token\nat 42"},
	}
	got := Summarize(dropped, 0)
	if len(got.Failures) != 1 {
		t.Fatalf("Failures = %q, want exactly one entry", got.Failures)
	}
	if !strings.Contains(got.Failures[0], "error: undefined: token") {
		t.Fatalf("Failures = %q, want the error line", got.Failures)
	}
	if strings.Contains(got.Failures[0], "compiling") {
		t.Fatalf("failure was taken from the first line: %q", got.Failures)
	}
	if !strings.Contains(got.Summary, "error: undefined: token") {
		t.Fatalf("summary dropped the failure:\n%s", got.Summary)
	}
}

func TestSummarizeKeepsMostRecentFilesByAppearanceOrder(t *testing.T) {
	// Appearance order: the three lexically largest paths are oldest, the
	// three lexically smallest are newest. Sorting before tailItems (the old
	// behaviour) would keep the wrong twelve.
	paths := []string{
		"z_old1.go", "z_old2.go", "z_old3.go",
		"m04.go", "m05.go", "m06.go", "m07.go", "m08.go",
		"m09.go", "m10.go", "m11.go", "m12.go",
		"a_new1.go", "a_new2.go", "a_new3.go",
	}
	var dropped []Entry
	for _, path := range paths {
		dropped = append(dropped, pathEntry(path))
	}
	got := Summarize(dropped, 0)
	if len(got.Files) != 12 {
		t.Fatalf("Files = %q, want 12 entries", got.Files)
	}
	seen := make(map[string]bool)
	for _, file := range got.Files {
		seen[file] = true
	}
	for _, want := range []string{"a_new1.go", "a_new2.go", "a_new3.go"} {
		if !seen[want] {
			t.Errorf("most recent file %q missing from Files: %q", want, got.Files)
		}
	}
	for _, stale := range []string{"z_old1.go", "z_old2.go", "z_old3.go"} {
		if seen[stale] {
			t.Errorf("oldest file %q should have been dropped, got Files: %q", stale, got.Files)
		}
	}
	// Display order is still sorted for a stable summary.
	if got.Files[0] != "a_new1.go" || got.Files[11] != "m12.go" {
		t.Errorf("Files not sorted for display: %q", got.Files)
	}
}

func TestSummarizeKeepsFirstLineMarkers(t *testing.T) {
	dropped := []Entry{
		{Role: "assistant", Content: "Next steps: 加 lexer 并补测试"},
		{Role: "tool", Name: "compile", Content: "error: build failed"},
	}
	got := Summarize(dropped, 0)
	if len(got.NextSteps) != 1 || !strings.Contains(got.NextSteps[0], "加 lexer") {
		t.Fatalf("first-line next step regressed: %q", got.NextSteps)
	}
	if len(got.Failures) != 1 || !strings.Contains(got.Failures[0], "error: build failed") {
		t.Fatalf("first-line failure regressed: %q", got.Failures)
	}
}

func TestSummarizeUTF8ChineseAndClipping(t *testing.T) {
	longStep := "下一步: " + strings.Repeat("补中文注释并跑测试", 60) // >200 runes
	dropped := []Entry{
		{Role: "user", Content: "目标：实现词法分析器"},
		{Role: "assistant", Content: "已完成解析器。\n" + longStep},
		{Role: "tool", Name: "编译", Content: "编译中...\n失败: 未定义的符号 token\n位置 42"},
	}
	got := Summarize(dropped, 0)

	if len(got.NextSteps) != 1 {
		t.Fatalf("NextSteps = %q, want exactly one entry", got.NextSteps)
	}
	step := got.NextSteps[0]
	if !strings.HasPrefix(step, "下一步:") {
		t.Fatalf("Chinese next-step marker lost: %q", step)
	}
	if !utf8.ValidString(step) {
		t.Fatalf("clipped next step is not valid UTF-8: %q", step)
	}
	if r := utf8.RuneCountInString(step); r > 200 {
		t.Fatalf("clipped next step has %d runes, want <= 200", r)
	}

	if len(got.Failures) != 1 || !strings.Contains(got.Failures[0], "失败: 未定义的符号 token") {
		t.Fatalf("Chinese failure on non-first line lost: %q", got.Failures)
	}
	if !strings.Contains(got.Summary, "目标：实现词法分析器") {
		t.Fatalf("UTF-8 goal lost in summary:\n%s", got.Summary)
	}
}
