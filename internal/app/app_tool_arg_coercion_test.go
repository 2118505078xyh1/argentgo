package app

import "testing"

// TestToolArgCoercionEnabledDefaultOn 守护默认开启决策（用户 2026-11-22：
// 能让 AI 更强就开）：nil env、未设置、无关值都启用，仅显式 "0" 紧急关闭。
func TestToolArgCoercionEnabledDefaultOn(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, true},
		{"unset", func(string) string { return "" }, true},
		{"unrelated value", func(string) string { return "1" }, true},
		{"explicit off", func(string) string { return "0" }, false},
		{"off with spaces", func(string) string { return " 0 " }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolArgCoercionEnabled(tc.env); got != tc.want {
				t.Fatalf("toolArgCoercionEnabled(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
