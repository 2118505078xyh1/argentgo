package app

import "testing"

// TestPromptHistoryEnabled 守住 ARGENT_TUI_PROMPT_HISTORY 的 fail-closed 语义：
// 仅当注入的 env 非 nil 且用精确变量名读出的值修剪后等于 "1" 时开启。
func TestPromptHistoryEnabled(t *testing.T) {
	const key = "ARGENT_TUI_PROMPT_HISTORY"
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, false},
		{"unset", func(string) string { return "" }, false},
		{"exact 1", func(string) string { return "1" }, true},
		{"padded 1", func(string) string { return " 1\t" }, true},
		{"zero", func(string) string { return "0" }, false},
		{"true is not 1", func(string) string { return "true" }, false},
		{"only exact key counts", func(k string) string {
			if k == key {
				return "1"
			}
			return ""
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptHistoryEnabled(tc.env); got != tc.want {
				t.Fatalf("promptHistoryEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}
