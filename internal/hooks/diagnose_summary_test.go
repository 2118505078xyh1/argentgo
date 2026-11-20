package hooks

import (
	"fmt"
	"strings"
	"testing"
)

// 诊断样本：两个 async 且 StopPropagation 的命令，夹着一个干净命令，
// 其中一条命令名含 CJK，用来同时检验问题数量、顺序与 UTF-8 保真。
func summarySampleConfig() *Config {
	return &Config{Commands: []Command{
		{Event: PreToolUse, Command: "写入守卫", Async: true, StopPropagation: true},
		{Event: PreToolUse, Command: "just-a-clean-hook"},
		{Event: Stop, Command: "通知用户", Async: true, StopPropagation: true},
	}}
}

// TestDiagnoseConfigSummaryNoIssues 验证没有问题（含 nil、空、干净、
// 单独 async、单独 StopPropagation 以及文档解析的场景）时返回空串，
// 使调用方可以整体省略该小节。
func TestDiagnoseConfigSummaryNoIssues(t *testing.T) {
	cases := []struct {
		name   string
		config *Config
	}{
		{name: "nil 配置", config: nil},
		{name: "空配置", config: &Config{}},
		{
			name: "干净配置",
			config: &Config{Commands: []Command{
				{Event: PreToolUse, Command: "echo"},
				{Event: Stop, Command: "notify"},
			}},
		},
		{
			name: "只有 async 不算问题",
			config: &Config{Commands: []Command{
				{Event: PostToolUse, Command: "后台通知", Async: true},
			}},
		},
		{
			name: "只有 StopPropagation 不算问题",
			config: &Config{Commands: []Command{
				{Event: PreToolUse, Command: "拒绝", StopPropagation: true},
			}},
		},
		{
			name: "文档解析无法设置内部标志",
			config: mustParseConfig(t, `{"hooks":{"PreToolUse":[
				{"matcher":"写入","hooks":[{"command":"echo 开始","async":true}]},
				{"command":"echo 结束"}
			]}}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiagnoseConfigSummary(tc.config); got != "" {
				t.Fatalf("DiagnoseConfigSummary() = %q, want empty string", got)
			}
		})
	}
}

// summaryHeaderAndLines 把汇总文本拆成汇总头与逐条问题行，便于测试
// 同时断言数量、行数与内容；空串拆成零行。
func summaryHeaderAndLines(t *testing.T, summary string) (string, []string) {
	t.Helper()
	if summary == "" {
		return "", nil
	}
	lines := strings.Split(summary, "\n")
	if len(lines) < 2 {
		t.Fatalf("summary %q has no issue lines", summary)
	}
	return lines[0], lines[1:]
}

// TestDiagnoseConfigSummarySingleIssue 验证单条问题时汇总头计数为 1，
// 且正文恰好是 FormatConfigIssues 输出的一行。
func TestDiagnoseConfigSummarySingleIssue(t *testing.T) {
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "guard", Async: true, StopPropagation: true},
	}}
	summary := DiagnoseConfigSummary(config)
	header, lines := summaryHeaderAndLines(t, summary)
	if want := "hook 配置问题: 1 项"; header != want {
		t.Fatalf("header = %q, want %q", header, want)
	}
	issues := DiagnoseConfig(config)
	if len(issues) != 1 {
		t.Fatalf("DiagnoseConfig() returned %d issues, want 1", len(issues))
	}
	if len(lines) != 1 || lines[0] != FormatConfigIssues(issues) {
		t.Fatalf("issue lines = %q, want %q", lines, FormatConfigIssues(issues))
	}
}

// TestDiagnoseConfigSummaryMultipleIssues 验证多条问题时汇总头计数正确、
// 行数与问题数一致，且顺序与 DiagnoseConfig 一致（复用 FormatConfigIssues）。
func TestDiagnoseConfigSummaryMultipleIssues(t *testing.T) {
	config := summarySampleConfig()
	issues := DiagnoseConfig(config)
	if len(issues) != 2 {
		t.Fatalf("DiagnoseConfig() returned %d issues, want 2", len(issues))
	}
	summary := DiagnoseConfigSummary(config)
	header, lines := summaryHeaderAndLines(t, summary)
	if want := "hook 配置问题: 2 项"; header != want {
		t.Fatalf("header = %q, want %q", header, want)
	}
	if len(lines) != len(issues) {
		t.Fatalf("got %d issue lines, want %d: %q", len(lines), len(issues), summary)
	}
	// 逐行比对，确保顺序与 DiagnoseConfig/FormatConfigIssues 完全一致。
	wantLines := strings.Split(FormatConfigIssues(issues), "\n")
	for i := range wantLines {
		if lines[i] != wantLines[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], wantLines[i])
		}
	}
	// 干净命令不应出现在汇总中。
	if strings.Contains(summary, "just-a-clean-hook") {
		t.Fatalf("summary should skip clean commands: %q", summary)
	}
}

// TestDiagnoseConfigSummaryCJK 验证 CJK 命令名与事件做逐字保留，不会被
// 截断成半个 UTF-8 字符，且汇总头计数仍然正确。
func TestDiagnoseConfigSummaryCJK(t *testing.T) {
	config := summarySampleConfig()
	summary := DiagnoseConfigSummary(config)
	for _, want := range []string{"hook 配置问题: 2 项", "写入守卫", "通知用户", "PreToolUse[0]", "Stop[2]"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q missing %q", summary, want)
		}
	}
	// 每条命令文本必须完整出现在正文中，不得被截断。
	for _, command := range []string{"写入守卫", "通知用户"} {
		if strings.Count(summary, command) != 1 {
			t.Fatalf("summary %q should contain %q exactly once", summary, command)
		}
	}
}

// TestDiagnoseConfigSummaryStableFormat 验证格式稳定：汇总头格式固定、
// 正文就是 FormatConfigIssues 的输出、末尾没有多余换行，且重复调用逐字节一致。
func TestDiagnoseConfigSummaryStableFormat(t *testing.T) {
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "guard", Async: true, StopPropagation: true},
	}}
	got := DiagnoseConfigSummary(config)
	want := fmt.Sprintf(diagnoseSummaryFormat, 1, FormatConfigIssues(DiagnoseConfig(config)))
	if got != want {
		t.Fatalf("DiagnoseConfigSummary() = %q, want %q", got, want)
	}
	if again := DiagnoseConfigSummary(config); again != got {
		t.Fatalf("summary is not stable across calls: %q vs %q", got, again)
	}
	if strings.HasSuffix(got, "\n") {
		t.Fatalf("summary has a trailing newline: %q", got)
	}
	header, lines := summaryHeaderAndLines(t, got)
	if header != "hook 配置问题: 1 项" || len(lines) != 1 {
		t.Fatalf("unstable shape: header=%q lines=%q", header, lines)
	}
	if !strings.HasPrefix(lines[0], "[async-stop-propagation] PreToolUse[0]: async command \"guard\" stops propagation") {
		t.Fatalf("unexpected issue line: %q", lines[0])
	}
}
