package main

import "testing"

// 只有前导的 `gui` 子命令能把 CLI 转入原生
// 前端。扫描每个参数会劫持提示和标志值。
func TestIsGUIRequestOnlyMatchesLeadingGUISubcommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"no args", nil, false},
		{"gui subcommand", []string{"gui"}, true},
		{"gui subcommand with flags", []string{"gui", "--model", "m1"}, true},
		{"prompt containing gui", []string{"fix", "gui", "bug"}, false},
		{"flag value named gui", []string{"--model", "gui", "hello"}, false},
		{"trailing gui prompt word", []string{"hello", "gui"}, false},
		{"gui flag", []string{"--gui"}, false},
		{"gui flag with prompt", []string{"--gui", "hello"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGUIRequest(tc.args); got != tc.want {
				t.Fatalf("isGUIRequest(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
