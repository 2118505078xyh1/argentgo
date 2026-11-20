package agent

import (
	"context"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestRecoverMangledToolName(t *testing.T) {
	available := []string{"shell", "read", "edit", "multi_edit", "github__create_issue", "执行"}
	tests := []struct {
		name      string
		mangled   string
		available []string
		want      string
	}{
		{"点分隔命名空间", "developer.shell", available, "shell"},
		{"双下划线命名空间", "developer__shell", available, "shell"},
		{"大小写混合", "Developer__Shell", available, "shell"},
		{"全大写模型写法", "SHELL", available, "shell"},
		{"首尾空白", "  developer . shell  ", available, "shell"},
		{"找不到返回原名", "developer.unknown", available, "developer.unknown"},
		{"CJK 工具名", "开发者.执行", available, "执行"},
		{"原名已经正确", "read", available, "read"},
		{"单下划线不切分", "my_shell", available, "my_shell"},
		{"漏写命名空间", "create_issue", available, "github__create_issue"},
		{"多级命名空间优先更具体的名字", "mcp.github.search", []string{"search", "github__search"}, "github__search"},
		{"混合分隔符", "developer.shell", []string{"developer__shell"}, "developer__shell"},
		{"后缀歧义保守返回原名", "shell", []string{"developer__shell", "other__shell"}, "shell"},
		{"大小写歧义保守返回原名", "a.b", []string{"A__B", "a__B"}, "a.b"},
		{"大小写变体存在原样匹配", "SHELL", []string{"SHELL", "shell"}, "SHELL"},
		{"空名字", "", available, ""},
		{"空候选集", "developer.shell", nil, "developer.shell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recoverMangledToolName(tt.mangled, tt.available)
			if got != tt.want {
				t.Fatalf("recoverMangledToolName(%q, %q) = %q, want %q", tt.mangled, tt.available, got, tt.want)
			}
		})
	}
}

func TestRecoverMangledToolNameDeterministic(t *testing.T) {
	available := []string{"shell", "github__create_issue", "执行"}
	cases := []struct {
		mangled string
		want    string
	}{
		{"developer.shell", "shell"},
		{"create_issue", "github__create_issue"},
		{"开发者.执行", "执行"},
		{"developer.unknown", "developer.unknown"},
	}
	for _, tt := range cases {
		first := recoverMangledToolName(tt.mangled, available)
		if first != tt.want {
			t.Fatalf("recoverMangledToolName(%q) = %q, want %q", tt.mangled, first, tt.want)
		}
		// 重复调用，以及打乱候选顺序，都必须得到同样的结果：
		// 还原逻辑不得依赖 map 迭代顺序或 available 的排列。
		reversed := make([]string, len(available))
		for i, a := range available {
			reversed[len(available)-1-i] = a
		}
		for i := 0; i < 20; i++ {
			if got := recoverMangledToolName(tt.mangled, available); got != first {
				t.Fatalf("第 %d 次调用得到 %q，首次为 %q", i, got, first)
			}
			if got := recoverMangledToolName(tt.mangled, reversed); got != first {
				t.Fatalf("候选顺序反转后得到 %q，首次为 %q", got, first)
			}
		}
	}
}

// TestToolNameRecoveryWiringDefaultOff 固定默认行为：未显式启用时，
// 派发看到的仍是模型原始工具名，且调用方持有的切片没有被改写。
func TestToolNameRecoveryWiringDefaultOff(t *testing.T) {
	SetToolNameRecovery(false)
	defer SetToolNameRecovery(false)

	ft := &fakeTools{}
	s := newRunState(Options{Tools: ft}, "go", nil)
	s.specs = []llm.ToolSpec{toolSpec("shell")}
	calls := []llm.ToolCall{toolCall("c1", "developer.shell", `{}`)}

	s.runTools(context.Background(), 0, calls)
	got := ft.names()
	if len(got) != 1 || got[0] != "developer.shell" {
		t.Fatalf("默认关闭时执行的工具名 = %q，want [developer.shell]", got)
	}
	if calls[0].Function.Name != "developer.shell" {
		t.Fatalf("原切片被改写：%q", calls[0].Function.Name)
	}
}

// TestToolNameRecoveryWiringWhenEnabled 验证 opt-in 路径：启用后，
// 在防护/审批/事件/执行之前统一使用还原后的真实名字，且不改动调用方切片。
func TestToolNameRecoveryWiringWhenEnabled(t *testing.T) {
	SetToolNameRecovery(true)
	defer SetToolNameRecovery(false)

	ft := &fakeTools{}
	s := newRunState(Options{Tools: ft}, "go", nil)
	s.specs = []llm.ToolSpec{toolSpec("shell"), toolSpec("read")}
	calls := []llm.ToolCall{toolCall("c1", "developer__shell", `{}`)}

	s.runTools(context.Background(), 0, calls)
	got := ft.names()
	if len(got) != 1 || got[0] != "shell" {
		t.Fatalf("启用后执行的工具名 = %q，want [shell]", got)
	}
	if calls[0].Function.Name != "developer__shell" {
		t.Fatalf("原切片被改写：%q", calls[0].Function.Name)
	}
}
