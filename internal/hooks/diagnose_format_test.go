package hooks

import (
	"fmt"
	"strings"
	"testing"
)

func TestFormatConfigIssuesEmpty(t *testing.T) {
	cases := []struct {
		name   string
		issues []ConfigIssue
	}{
		{name: "nil slice", issues: nil},
		{name: "empty slice", issues: []ConfigIssue{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatConfigIssues(tc.issues); got != "" {
				t.Fatalf("FormatConfigIssues() = %q, want empty string", got)
			}
		})
	}
}

func TestFormatConfigIssuesFormatsIssue(t *testing.T) {
	issue := ConfigIssue{
		Kind:    IssueAsyncStopPropagation,
		Event:   PreToolUse,
		Index:   3,
		Command: "guard",
		Message: "later hooks are skipped",
	}
	want := "[async-stop-propagation] PreToolUse[3]: later hooks are skipped"
	if got := FormatConfigIssues([]ConfigIssue{issue}); got != want {
		t.Fatalf("FormatConfigIssues() = %q, want %q", got, want)
	}
}

func TestFormatConfigIssuesPreservesOrder(t *testing.T) {
	issues := []ConfigIssue{
		{Kind: IssueAsyncStopPropagation, Event: SessionStart, Index: 0, Command: "first", Message: "first problem"},
		{Kind: IssueAsyncStopPropagation, Event: PreToolUse, Index: 5, Command: "high", Message: "second problem"},
		{Kind: IssueAsyncStopPropagation, Event: Stop, Index: 9, Command: "last", Message: "third problem"},
	}
	want := []string{
		"[async-stop-propagation] SessionStart[0]: first problem",
		"[async-stop-propagation] PreToolUse[5]: second problem",
		"[async-stop-propagation] Stop[9]: third problem",
	}
	got := FormatConfigIssues(issues)
	lines := strings.Split(got, "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), got)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestFormatConfigIssuesCJK(t *testing.T) {
	issues := []ConfigIssue{
		{
			Kind:    IssueAsyncStopPropagation,
			Event:   PreToolUse,
			Index:   2,
			Command: "写入守卫",
			Message: fmt.Sprintf("async command %q stops propagation", "写入守卫"),
		},
		{
			Kind:    IssueAsyncStopPropagation,
			Event:   Stop,
			Index:   7,
			Command: "通知用户",
			Message: "通知用户 hook 同步决策",
		},
	}
	got := FormatConfigIssues(issues)
	for _, want := range []string{"写入守卫", "通知用户", "PreToolUse[2]", "Stop[7]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q missing %q", got, want)
		}
	}
}

func TestFormatConfigIssuesFromDiagnose(t *testing.T) {
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "clean"},
		{Event: PreToolUse, Command: "写入守卫", Async: true, StopPropagation: true},
		{Event: Stop, Command: "最后通知", Async: true, StopPropagation: true},
	}}
	text := FormatConfigIssues(DiagnoseConfig(config))
	for _, want := range []string{
		"async-stop-propagation",
		"PreToolUse[1]",
		"Stop[2]",
		"写入守卫",
		"最后通知",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output %q missing %q", text, want)
		}
	}
	if lines := strings.Split(text, "\n"); len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), text)
	}
}

func TestFormatConfigIssuesCleanConfigIsEmpty(t *testing.T) {
	config := &Config{Commands: []Command{
		{Event: PreToolUse, Command: "echo"},
		{Event: Stop, Command: "notify"},
	}}
	if got := FormatConfigIssues(DiagnoseConfig(config)); got != "" {
		t.Fatalf("clean config formatted = %q, want empty", got)
	}
}
