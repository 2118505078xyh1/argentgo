package agent

import (
	"strings"
	"testing"
)

func TestAppendToolErrorOutput(t *testing.T) {
	// 每个子测试显式设门控，避免 init() 默认值与并行测试相互干扰。
	defer SetToolErrorOutput(true)
	cases := []struct {
		name          string
		enabled       bool
		errText       string
		output        string
		want          string
		wantUnchanged bool
	}{
		{
			name: "merge stderr root cause", enabled: true,
			errText: "error: bash: exit status 1",
			output:  "shell: cd: not a directory: /d/code\n",
			want:    "error: bash: exit status 1\nshell: cd: not a directory: /d/code",
		},
		{
			name: "empty output unchanged", enabled: true,
			errText: "error: bash: exit status 1", output: "   \n",
			wantUnchanged: true,
		},
		{
			name: "already contained unchanged", enabled: true,
			errText:       "error: bash: exit status 1\nshell: cd: not a directory",
			output:        "shell: cd: not a directory",
			wantUnchanged: true,
		},
		{
			name: "gate off unchanged", enabled: false,
			errText: "error: bash: exit status 1", output: "shell: cd: not a directory",
			wantUnchanged: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetToolErrorOutput(tc.enabled)
			got := appendToolErrorOutput(tc.errText, tc.output)
			if tc.wantUnchanged {
				if got != tc.errText {
					t.Fatalf("got %q, want unchanged %q", got, tc.errText)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	SetToolErrorOutput(true)
}

func TestAppendToolErrorOutputBounded(t *testing.T) {
	SetToolErrorOutput(true)
	defer SetToolErrorOutput(true)
	got := appendToolErrorOutput("error: x", strings.Repeat("A", toolErrorOutputMaxBytes*3))
	if len(got) > len("error: x\n")+toolErrorOutputMaxBytes+64 {
		t.Fatalf("output not bounded: %d bytes", len(got))
	}
}
