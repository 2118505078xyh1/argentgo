package app

import "testing"

// TestPromptHistoryEnabled 守住 ARGENT_TUI_PROMPT_HISTORY 的「默认开、=0 关」语义：
// env 为 nil 或值修剪后不等于 "0" 时开启；仅修剪后精确等于 "0" 时关闭。
func TestPromptHistoryEnabled(t *testing.T) {
	const key = "ARGENT_TUI_PROMPT_HISTORY"
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, true},
		{"unset", func(string) string { return "" }, true},
		{"exact 0", func(string) string { return "0" }, false},
		{"padded 0", func(string) string { return " 0\t" }, false},
		{"exact 1", func(string) string { return "1" }, true},
		{"padded 1", func(string) string { return " 1\t" }, true},
		{"true is not 0", func(string) string { return "true" }, true},
		// 读取的错误键返回 "0"：若门控取了别的变量名就会被关掉，从而锁定精确键名。
		{"only exact key counts", func(k string) string {
			if k == key {
				return "1"
			}
			return "0"
		}, true},
		// 只有精确键的 "0" 能关闭，其它键的 "0" 不影响。
		{"only exact key can disable", func(k string) string {
			if k == key {
				return "0"
			}
			return "1"
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptHistoryEnabled(tc.env); got != tc.want {
				t.Fatalf("promptHistoryEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}
