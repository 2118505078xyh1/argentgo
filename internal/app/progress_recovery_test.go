package app

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// TestProgressRecoveryEnabledDefaultOn 守护默认开启决策：nil/未设置/无关值都启用，
// 仅 ARGENT_PROGRESS_RECOVERY=0 紧急关闭。与 tool 强转开关同构。
func TestProgressRecoveryEnabledDefaultOn(t *testing.T) {
	cases := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil env", nil, true},
		{"unset", func(string) string { return "" }, true},
		{"unrelated", func(string) string { return "yes" }, true},
		{"off", func(string) string { return "0" }, false},
		{"off with spaces", func(string) string { return " 0 " }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := progressRecoveryEnabled(tc.env); got != tc.want {
				t.Fatalf("progressRecoveryEnabled(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestProgressHintText 守护恢复公告文本：包含轮次、未完成输出与工具调用，
// 且只用于用户可见公告（这里断言其形状稳定）。
func TestProgressHintText(t *testing.T) {
	p := session.Progress{Turn: 3, AssistantText: "  partial answer  ", ToolCalls: []string{"bash", "read"}}
	got := progressHintText(p)
	for _, want := range []string{"第 3 轮", "partial answer", "bash", "read"} {
		if !strings.Contains(got, want) {
			t.Errorf("progressHintText 缺少 %q: %q", want, got)
		}
	}
	// 文本被裁剪了两侧空白（strings.TrimSpace 生效）。
	if strings.Contains(got, "  partial") {
		t.Errorf("未完成输出未去空白: %q", got)
	}
}

// TestProgressHintTextEmpty 守护空进度不产生误导性公告。
func TestProgressHintTextEmpty(t *testing.T) {
	got := progressHintText(session.Progress{Turn: 1})
	if !strings.Contains(got, "第 1 轮") || strings.Contains(got, "未完成输出") {
		t.Fatalf("空进度的公告形状异常: %q", got)
	}
}
