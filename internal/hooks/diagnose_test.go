package hooks

import (
	"reflect"
	"strings"
	"testing"
)

// issueKey 是 ConfigIssue 中用于表格比较的稳定部分；
// Message 单独检查（非空，且提到该命令）。
type issueKey struct {
	Kind    IssueKind
	Event   Event
	Index   int
	Command string
}

func TestDiagnoseConfigAsyncStopPropagation(t *testing.T) {
	cases := []struct {
		name   string
		config *Config
		want   []issueKey
	}{
		{
			name:   "nil config",
			config: nil,
			want:   nil,
		},
		{
			name:   "empty config",
			config: &Config{},
			want:   nil,
		},
		{
			name: "clean config with CJK command names",
			config: &Config{Commands: []Command{
				{Event: PreToolUse, Command: "备份文件"},
				{Event: PreToolUse, Command: "检查权限", Async: true},
				{Event: Stop, Command: "通知用户", StopPropagation: true},
				{Event: FileChanged, Command: "记录变更", Priority: 5},
			}},
			want: nil,
		},
		{
			name: "document parse cannot set the internal flags",
			config: mustParseConfig(t, `{"hooks":{"PreToolUse":[
				{"matcher":"写入","hooks":[{"command":"echo 开始","async":true}]},
				{"command":"echo 结束"}
			]}}`),
			want: nil,
		},
		{
			name: "single async stop command",
			config: &Config{Commands: []Command{
				{Event: PreToolUse, Command: "guard", Async: true, StopPropagation: true},
			}},
			want: []issueKey{{
				Kind: IssueAsyncStopPropagation, Event: PreToolUse, Index: 0, Command: "guard",
			}},
		},
		{
			name: "CJK command name with the combination",
			config: &Config{Commands: []Command{
				{Event: PreToolUse, Command: "写入守卫", Async: true, StopPropagation: true},
			}},
			want: []issueKey{{
				Kind: IssueAsyncStopPropagation, Event: PreToolUse, Index: 0, Command: "写入守卫",
			}},
		},
		{
			name: "async alone is fine",
			config: &Config{Commands: []Command{
				{Event: PostToolUse, Command: "后台通知", Async: true},
			}},
			want: nil,
		},
		{
			name: "stop propagation alone is fine",
			config: &Config{Commands: []Command{
				{Event: PreToolUse, Command: "拒绝", StopPropagation: true},
			}},
			want: nil,
		},
		{
			name: "combination is reported at its own index",
			config: &Config{Commands: []Command{
				{Event: SessionStart, Command: "first"},
				{Event: PreToolUse, Command: "high", Async: true, StopPropagation: true},
				{Event: PreToolUse, Command: "low"},
			}},
			want: []issueKey{{
				Kind: IssueAsyncStopPropagation, Event: PreToolUse, Index: 1, Command: "high",
			}},
		},
		{
			name: "multiple combinations across events",
			config: &Config{Commands: []Command{
				{Event: PreToolUse, Command: "写入守卫", Async: true, StopPropagation: true},
				{Event: Stop, Command: "记录结束", StopPropagation: true},
				{Event: Stop, Command: "通知", Async: true, StopPropagation: true},
			}},
			want: []issueKey{
				{Kind: IssueAsyncStopPropagation, Event: PreToolUse, Index: 0, Command: "写入守卫"},
				{Kind: IssueAsyncStopPropagation, Event: Stop, Index: 2, Command: "通知"},
			},
		},
		{
			name: "programmatic event outside AllEvents is still flagged",
			config: &Config{Commands: []Command{
				{Event: "CustomGuard", Command: "自定义钩子", Async: true, StopPropagation: true},
			}},
			want: []issueKey{{
				Kind: IssueAsyncStopPropagation, Event: "CustomGuard", Index: 0, Command: "自定义钩子",
			}},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := DiagnoseConfig(test.config)
			var keys []issueKey
			for _, issue := range got {
				keys = append(keys, issueKey{
					Kind: issue.Kind, Event: issue.Event, Index: issue.Index, Command: issue.Command,
				})
				if issue.Message == "" {
					t.Errorf("issue %+v has an empty Message", issue)
				}
				if !strings.Contains(issue.Message, issue.Command) {
					t.Errorf("Message %q does not mention command %q", issue.Message, issue.Command)
				}
			}
			want := test.want
			if len(want) == 0 {
				want = nil
			}
			if !reflect.DeepEqual(keys, want) {
				t.Fatalf("DiagnoseConfig() keys = %+v, want %+v", keys, want)
			}
		})
	}
}

func TestConfigIssueString(t *testing.T) {
	issue := ConfigIssue{
		Kind: IssueAsyncStopPropagation, Event: PreToolUse, Index: 3, Command: "guard",
		Message: "later hooks are skipped",
	}
	want := "[async-stop-propagation] PreToolUse[3]: later hooks are skipped"
	if got := issue.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func mustParseConfig(t *testing.T, doc string) *Config {
	t.Helper()
	config, err := ParseConfig([]byte(doc))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	return config
}
