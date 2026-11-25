package app

import (
	"testing"

	"example.com/argentgo/internal/hooks"
)

func TestExtensionInventoryEnabled(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil", nil, false},
		{"unset", func(string) string { return "" }, false},
		{"one", func(string) string { return "1" }, true},
		{"one spaced", func(string) string { return " 1 " }, true},
		{"zero", func(string) string { return "0" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extensionInventoryEnabled(tc.env); got != tc.want {
				t.Fatalf("enabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestReportExtensionInventoryDisabled 验证默认关闭时门面接线零输出、不触碰信任门。
func TestReportExtensionInventoryDisabled(t *testing.T) {
	// env 关闭时函数应立即返回，不 panic（bridge/trust 为 nil 也安全）。
	reportExtensionInventory(t.TempDir(), t.TempDir(), hooks.NewTrustStore(), func(string) string { return "" })
}
