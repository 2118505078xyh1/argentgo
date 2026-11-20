package proc

import (
	"strings"
	"testing"
	"time"
)

func TestCommandSummary(t *testing.T) {
	tests := []struct {
		name string
		cmd  *Command
		want string
	}{
		{
			name: "nil command",
			cmd:  nil,
			want: "<nil>",
		},
		{
			name: "empty command",
			cmd:  &Command{},
			want: `cmd="" timeout=none killTree=false env=0`,
		},
		{
			name: "plain command with args",
			cmd:  &Command{Path: "go", Args: []string{"test", "./..."}},
			want: `cmd="go test ./..." timeout=none killTree=false env=0`,
		},
		{
			name: "CJK command and args stay readable",
			cmd:  &Command{Path: "中文命令", Args: []string{"参数", "你好 世界"}},
			want: `cmd="中文命令 参数 你好 世界" timeout=none killTree=false env=0`,
		},
		{
			name: "timeout and kill tree mode",
			cmd: &Command{
				Path:     "sleep",
				Args:     []string{"10"},
				Timeout:  5 * time.Second,
				KillTree: true,
			},
			want: `cmd="sleep 10" timeout=5s killTree=true env=0`,
		},
		{
			name: "subsecond timeout",
			cmd: &Command{
				Path:    "sleep",
				Timeout: 500 * time.Millisecond,
			},
			want: `cmd="sleep" timeout=500ms killTree=false env=0`,
		},
		{
			name: "credential arguments are redacted",
			cmd: &Command{
				Path: "tool",
				Args: []string{
					"--token=abc123",
					"--password=hunter2",
					"API_KEY=xyz",
					"--normal=visible",
				},
			},
			want: `cmd="tool --token=*** --password=*** API_KEY=*** --normal=visible" timeout=none killTree=false env=0`,
		},
		{
			name: "secret keys are case insensitive and separator agnostic",
			cmd: &Command{
				Path: "tool",
				Args: []string{"--API-KEY=k1", "access-key=k2", "PRIVATE_KEY=k3", "MyToken=k4"},
			},
			want: `cmd="tool --API-KEY=*** access-key=*** PRIVATE_KEY=*** MyToken=***" timeout=none killTree=false env=0`,
		},
		{
			name: "flags without values and empty values pass through",
			cmd: &Command{
				Path: "tool",
				Args: []string{"--secret", "--token=", "--verbose"},
			},
			want: `cmd="tool --secret --token= --verbose" timeout=none killTree=false env=0`,
		},
		{
			name: "value containing equals is fully redacted",
			cmd: &Command{
				Path: "tool",
				Args: []string{"--token=a=b=c"},
			},
			want: `cmd="tool --token=***" timeout=none killTree=false env=0`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CommandSummary(tt.cmd); got != tt.want {
				t.Fatalf("CommandSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommandSummaryNeverRevealsEnvValues(t *testing.T) {
	// Env 值只计数、不打印，因此环境中的凭据
	// 不会通过摘要泄露。
	c := &Command{
		Path: "tool",
		Env:  []string{"API_KEY=supersecret-value", "REGION=cn-north"},
	}
	got := CommandSummary(c)
	if strings.Contains(got, "supersecret-value") || strings.Contains(got, "cn-north") {
		t.Fatalf("CommandSummary leaked an env value: %q", got)
	}
	if !strings.Contains(got, "env=2") {
		t.Fatalf("CommandSummary = %q, want env=2", got)
	}
}

func TestCommandSummaryDoesNotMutateCommand(t *testing.T) {
	args := []string{"--token=abc123", "plain"}
	c := &Command{Path: "tool", Args: args}
	_ = CommandSummary(c)
	if args[0] != "--token=abc123" || args[1] != "plain" {
		t.Fatalf("CommandSummary mutated args: %q", args)
	}
}
