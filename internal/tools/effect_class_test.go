package tools

import "testing"

// TestClassifyEffectLocalReads 检查本地纯读白名单在 mutating=false 时
// 全部判为 local-read —— 这是唯一可推测执行的类别。
func TestClassifyEffectLocalReads(t *testing.T) {
	for _, name := range []string{
		"read", "ls", "glob", "grep", "find", "git_status", "repo_map", "diff",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ClassifyEffect(name, false); got != EffectLocalRead {
				t.Fatalf("ClassifyEffect(%q, false) = %q, want %q", name, got, EffectLocalRead)
			}
		})
	}
}

// TestClassifyEffectExternalReads 检查外部数据源与记忆类工具
// 在 mutating=false 时判为 external-read。memory_ 为前缀匹配。
func TestClassifyEffectExternalReads(t *testing.T) {
	tests := []struct {
		name string
		want EffectClass
	}{
		{"web_fetch", EffectExternalRead},
		{"web_search", EffectExternalRead},
		{"current_time", EffectExternalRead},
		{"memory_retain", EffectExternalRead},
		{"memory_recall", EffectExternalRead},
		{"memory_forget", EffectExternalRead},
		{"memory_read", EffectExternalRead},
		{"memory_", EffectExternalRead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyEffect(tc.name, false); got != tc.want {
				t.Fatalf("ClassifyEffect(%q, false) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestClassifyEffectForbidden 检查危险名黑名单优先于 mutating 标志：
// 任意命令执行无论是否声明为变更，都判为 forbidden。
func TestClassifyEffectForbidden(t *testing.T) {
	for _, name := range []string{"bash", "powershell", "shell", "exec"} {
		for _, mutating := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				if got := ClassifyEffect(name, mutating); got != EffectForbidden {
					t.Fatalf("ClassifyEffect(%q, %t) = %q, want %q", name, mutating, got, EffectForbidden)
				}
			})
		}
	}
}

// TestClassifyEffectMutatingCombinations 检查 mutating 标志的作用：
// 非危险名一旦是变更操作即判为 side-effect，覆盖原本落在读白名单
// 或外部读白名单里的名字，从而杜绝把写操作当成纯读推测执行。
func TestClassifyEffectMutatingCombinations(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
	}{
		{"local read name", "read"},
		{"repo map name", "repo_map"},
		{"diff name", "diff"},
		{"external read name", "web_fetch"},
		{"current time name", "current_time"},
		{"memory prefix name", "memory_forget"},
		{"write name", "write"},
		{"edit name", "edit"},
		{"multi edit name", "multi_edit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyEffect(tc.toolName, true); got != EffectSideEffect {
				t.Fatalf("ClassifyEffect(%q, true) = %q, want %q", tc.toolName, got, EffectSideEffect)
			}
		})
	}
}

// TestClassifyEffectWhitelistBoundaries 检查白名单边界：
// 精确、大小写敏感、不做 trim。任何看起来像白名单名但不完全相同的
// 名字都必须落到保守的 external-read，而不能混入 local-read。
func TestClassifyEffectWhitelistBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
	}{
		{"trailing space", "read "},
		{"leading space", " read"},
		{"trailing newline", "read\n"},
		{"uppercase", "READ"},
		{"mixed case", "Git_Status"},
		{"suffix", "reader"},
		{"prefix", "re"},
		{"memory without underscore", "memory"},
		{"memory with space", "memory_read "},
		{"web fetch suffix", "web_fetch_all"},
		{"empty name", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyEffect(tc.toolName, false); got != EffectExternalRead {
				t.Fatalf("ClassifyEffect(%q, false) = %q, want conservative %q", tc.toolName, got, EffectExternalRead)
			}
		})
	}
}

// TestClassifyEffectUnknownNamesConservative 检查未知名与 CJK 名的
// 保守归类：非变更落到 external-read，变更落到 side-effect，
// 两条路径都不会得到 local-read。
func TestClassifyEffectUnknownNamesConservative(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		wantRead     EffectClass
		wantMutating EffectClass
	}{
		{"CJK name", "读取", EffectExternalRead, EffectSideEffect},
		{"CJK tool", "工具", EffectExternalRead, EffectSideEffect},
		{"emoji suffix", "read😀", EffectExternalRead, EffectSideEffect},
		{"arbitrary name", "unknown_tool_xyz", EffectExternalRead, EffectSideEffect},
		{"new future tool", "future_tool", EffectExternalRead, EffectSideEffect},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyEffect(tc.toolName, false); got != tc.wantRead {
				t.Fatalf("ClassifyEffect(%q, false) = %q, want %q", tc.toolName, got, tc.wantRead)
			}
			if got := ClassifyEffect(tc.toolName, true); got != tc.wantMutating {
				t.Fatalf("ClassifyEffect(%q, true) = %q, want %q", tc.toolName, got, tc.wantMutating)
			}
		})
	}
}

// TestSpeculativeAllowed 检查只有 local-read 可推测执行；
// 其余所有类别、未知取值与零值全部 false（fail-closed）。
func TestSpeculativeAllowed(t *testing.T) {
	tests := []struct {
		name  string
		class EffectClass
		want  bool
	}{
		{"local read", EffectLocalRead, true},
		{"external read", EffectExternalRead, false},
		{"idempotent write", EffectIdempotentWrite, false},
		{"side effect", EffectSideEffect, false},
		{"forbidden", EffectForbidden, false},
		{"zero value", EffectClass(""), false},
		{"unknown value", EffectClass("brand-new"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SpeculativeAllowed(tc.class); got != tc.want {
				t.Fatalf("SpeculativeAllowed(%q) = %t, want %t", tc.class, got, tc.want)
			}
		})
	}
}

// TestEffectClassConstantsDistinct 检查五个类别常量互不相等，
// 保证分类体系没有重复的枚举值。
func TestEffectClassConstantsDistinct(t *testing.T) {
	all := []EffectClass{
		EffectLocalRead,
		EffectExternalRead,
		EffectIdempotentWrite,
		EffectSideEffect,
		EffectForbidden,
	}
	seen := make(map[EffectClass]bool, len(all))
	for _, c := range all {
		if seen[c] {
			t.Fatalf("duplicate EffectClass value %q", c)
		}
		seen[c] = true
	}
}
