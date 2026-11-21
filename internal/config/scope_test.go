package config

import (
	"strings"
	"testing"
)

// TestScopePriorityCanonical 覆盖五个规范作用域：返回值必须严格等于
// 设计中的 0..4，并且彼此严格递增（default < user < project < env < flag）。
func TestScopePriorityCanonical(t *testing.T) {
	cases := []struct {
		scope string
		want  int
	}{
		{scope: "default", want: 0},
		{scope: "user", want: 1},
		{scope: "project", want: 2},
		{scope: "env", want: 3},
		{scope: "flag", want: 4},
	}
	previous := -1
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			got := scopePriority(tc.scope)
			if got != tc.want {
				t.Fatalf("scopePriority(%q) = %d, want %d", tc.scope, got, tc.want)
			}
			if got <= previous {
				t.Errorf("优先级未严格递增: scopePriority(%q) = %d, 前一个为 %d", tc.scope, got, previous)
			}
		})
		previous = scopePriority(tc.scope)
	}
}

// TestScopePriorityAliases 覆盖别名：global/local/cli 必须分别映射到
// user/project/flag 的优先级，而非未知值。
func TestScopePriorityAliases(t *testing.T) {
	cases := []struct {
		alias string
		want  int
	}{
		{alias: "global", want: scopePriority("user")},
		{alias: "local", want: scopePriority("project")},
		{alias: "cli", want: scopePriority("flag")},
	}
	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			if got := scopePriority(tc.alias); got != tc.want {
				t.Errorf("scopePriority(%q) = %d, want %d", tc.alias, got, tc.want)
			}
		})
	}
}

// TestScopePriorityCaseInsensitive 覆盖大小写不敏感：全大写、全小写、
// 混合大小写以及别名的变体都必须得到与规范拼写相同的结果。
func TestScopePriorityCaseInsensitive(t *testing.T) {
	cases := []struct {
		scope string
		want  int
	}{
		{scope: "DEFAULT", want: 0},
		{scope: "DeFaUlT", want: 0},
		{scope: "USER", want: 1},
		{scope: "User", want: 1},
		{scope: "PROJECT", want: 2},
		{scope: "Project", want: 2},
		{scope: "ENV", want: 3},
		{scope: "Env", want: 3},
		{scope: "FLAG", want: 4},
		{scope: "Flag", want: 4},
		{scope: "GLOBAL", want: 1},
		{scope: "Local", want: 2},
		{scope: "CLI", want: 4},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			if got := scopePriority(tc.scope); got != tc.want {
				t.Errorf("scopePriority(%q) = %d, want %d", tc.scope, got, tc.want)
			}
		})
	}
}

// TestScopePriorityTrimsWhitespace 覆盖首尾空白的忽略：ASCII 空格、
// 制表符以及全角空格（U+3000）都必须被剥离后再匹配。
func TestScopePriorityTrimsWhitespace(t *testing.T) {
	cases := []struct {
		name  string
		scope string
		want  int
	}{
		{name: "ascii spaces", scope: "  flag  ", want: 4},
		{name: "tabs", scope: "\tuser\n", want: 1},
		{name: "full-width spaces", scope: "\u3000project\u3000", want: 2},
		{name: "mixed trim and case", scope: " \u3000DeFaUlT\t", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopePriority(tc.scope); got != tc.want {
				t.Errorf("scopePriority(%q) = %d, want %d", tc.scope, got, tc.want)
			}
		})
	}
}

// TestScopePriorityUnknown 覆盖各类未知输入：空串、纯空白、普通未知词、
// 近似拼写、超长串，以及含 CJK 字符的名称都必须返回 -1。CJK 用例同时
// 验证 CJK 安全：多字节字符既不会让函数 panic，也不会被错误截断后
// 误判成已知作用域。
func TestScopePriorityUnknown(t *testing.T) {
	cases := []struct {
		name  string
		scope string
	}{
		{name: "empty", scope: ""},
		{name: "whitespace only", scope: "   \t\u3000"},
		{name: "ordinary word", scope: "model"},
		{name: "near miss defaults", scope: "defaults"},
		{name: "near miss environment", scope: "environment"},
		{name: "near miss flags", scope: "flags"},
		{name: "CJK default", scope: "默认"},
		{name: "CJK user", scope: "用户"},
		{name: "CJK project", scope: "项目"},
		{name: "CJK env", scope: "环境"},
		{name: "CJK flag", scope: "命令行"},
		{name: "CJK phrase", scope: "配置作用域"},
		{name: "mixed CJK and ascii", scope: "用户flag"},
		{name: "oversized", scope: strings.Repeat("flag", 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scopePriority(tc.scope); got != -1 {
				t.Errorf("scopePriority(%q) = %d, want -1", tc.scope, got)
			}
		})
	}
}

// TestScopePriorityIsPure 验证纯函数约定：对已知和未知作用域重复调用，
// 结果都必须一致，且不因调用次数而改变。
func TestScopePriorityIsPure(t *testing.T) {
	for _, scope := range []string{"project", "CLI", "不存在"} {
		want := scopePriority(scope)
		for i := 0; i < 3; i++ {
			if got := scopePriority(scope); got != want {
				t.Fatalf("scopePriority(%q) 第 %d 次调用结果不一致: %d vs %d", scope, i, got, want)
			}
		}
	}
}
