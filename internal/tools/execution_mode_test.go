package tools_test

import (
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// TestDeclaredMode 覆盖静态声明：少数本地纯读工具为 shared，
// 其余（含 usage/history/lsp_*/memory_*）一律为零值未声明。
func TestDeclaredMode(t *testing.T) {
	cases := []struct {
		name string
		want tools.ExecutionMode
	}{
		// 声明项：本地纯读工具。
		{"read", tools.ExecutionModeShared},
		{"ls", tools.ExecutionModeShared},
		{"glob", tools.ExecutionModeShared},
		{"grep", tools.ExecutionModeShared},
		{"find", tools.ExecutionModeShared},
		{"git_status", tools.ExecutionModeShared},
		{"repo_map", tools.ExecutionModeShared},
		{"diff", tools.ExecutionModeShared},
		// 非声明项：有状态、副作用或依赖外部数据源。
		{"usage", tools.ExecutionModeUnspecified},
		{"history", tools.ExecutionModeUnspecified},
		{"lsp_symbols", tools.ExecutionModeUnspecified},
		{"lsp_references", tools.ExecutionModeUnspecified},
		{"lsp_code_actions", tools.ExecutionModeUnspecified},
		{"lsp_rename_preview", tools.ExecutionModeUnspecified},
		{"lsp_format", tools.ExecutionModeUnspecified},
		{"lsp_workspace_symbols", tools.ExecutionModeUnspecified},
		{"memory_retain", tools.ExecutionModeUnspecified},
		{"memory_recall", tools.ExecutionModeUnspecified},
		{"memory_forget", tools.ExecutionModeUnspecified},
		{"memory_reflect", tools.ExecutionModeUnspecified},
		{"memory_learn", tools.ExecutionModeUnspecified},
		{"write", tools.ExecutionModeUnspecified},
		{"edit", tools.ExecutionModeUnspecified},
		{"bash", tools.ExecutionModeUnspecified},
		{"powershell", tools.ExecutionModeUnspecified},
		{"unknown_tool", tools.ExecutionModeUnspecified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tools.DeclaredMode(tc.name); got != tc.want {
				t.Fatalf("DeclaredMode(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
	if got := tools.DeclaredMode(""); got != tools.ExecutionModeUnspecified {
		t.Fatalf("DeclaredMode(\"\") = %q, want unspecified", got)
	}
}

// TestAllDeclaredModesCopy 验证返回的是副本，且恰好包含预期的声明集合。
func TestAllDeclaredModesCopy(t *testing.T) {
	want := map[string]tools.ExecutionMode{
		"read":       tools.ExecutionModeShared,
		"ls":         tools.ExecutionModeShared,
		"glob":       tools.ExecutionModeShared,
		"grep":       tools.ExecutionModeShared,
		"find":       tools.ExecutionModeShared,
		"git_status": tools.ExecutionModeShared,
		"repo_map":   tools.ExecutionModeShared,
		"diff":       tools.ExecutionModeShared,
	}

	first := tools.AllDeclaredModes()
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("AllDeclaredModes() = %v, want %v", first, want)
	}

	// 修改副本：既不能影响声明表，也不能影响另一份副本。
	first["read"] = tools.ExecutionModeExclusive
	first["usage"] = tools.ExecutionModeShared
	delete(first, "ls")

	if got := tools.DeclaredMode("read"); got != tools.ExecutionModeShared {
		t.Fatalf("mutating copy changed declared read = %q", got)
	}
	if got := tools.DeclaredMode("usage"); got != tools.ExecutionModeUnspecified {
		t.Fatalf("mutating copy changed declared usage = %q", got)
	}
	if got := tools.DeclaredMode("ls"); got != tools.ExecutionModeShared {
		t.Fatalf("mutating copy changed declared ls = %q", got)
	}

	second := tools.AllDeclaredModes()
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("AllDeclaredModes() after mutation = %v, want %v", second, want)
	}
	if first["read"] != tools.ExecutionModeExclusive {
		t.Fatal("the two returned maps share storage")
	}
}

// TestSetExecutionModePerRegistry 验证覆盖是逐注册表实例的，
// 另一个 New() 看不到，且运行时覆盖不污染静态声明表。
func TestSetExecutionModePerRegistry(t *testing.T) {
	root := t.TempDir()
	a := tools.New(root, true)
	b := tools.New(root, true)

	cases := []struct {
		name     string
		mode     tools.ExecutionMode
		declared tools.ExecutionMode
	}{
		{"read", tools.ExecutionModeExclusive, tools.ExecutionModeShared},
		{"usage", tools.ExecutionModeShared, tools.ExecutionModeUnspecified},
		{"bash", tools.ExecutionModeExclusive, tools.ExecutionModeUnspecified},
	}
	for _, tc := range cases {
		a.SetExecutionMode(tc.name, tc.mode)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.ExecutionMode(tc.name); got != tc.mode {
				t.Fatalf("a.ExecutionMode(%q) = %q, want %q", tc.name, got, tc.mode)
			}
			if got := b.ExecutionMode(tc.name); got != tools.ExecutionModeUnspecified {
				t.Fatalf("b.ExecutionMode(%q) = %q, want unspecified (override leaked)", tc.name, got)
			}
			if got := tools.DeclaredMode(tc.name); got != tc.declared {
				t.Fatalf("DeclaredMode(%q) = %q, want %q (override polluted declaration)", tc.name, got, tc.declared)
			}
		})
	}

	// 全新注册表的每项执行方式都应为零值：默认全部未声明。
	for _, name := range b.Names() {
		if got := b.ExecutionMode(name); got != tools.ExecutionModeUnspecified {
			t.Fatalf("fresh registry ExecutionMode(%q) = %q, want unspecified", name, got)
		}
	}
}

// TestSpecsOrderMatchesGolden 验证新增声明不改动 Specs()/Names() 的顺序，
// golden 与 builtin_register_test.go 中锁定的顺序逐字节一致。
func TestSpecsOrderMatchesGolden(t *testing.T) {
	golden := []string{
		"read", "write", "edit", "bash", "ls", "grep", "find", "powershell",
		"glob", "todo", "multi_edit", "web_fetch", "git_status", "current_time",
		"web_search", "repo_map", "submit_plan",
		"lsp_symbols", "lsp_references", "lsp_code_actions", "lsp_rename_preview",
		"lsp_format", "lsp_workspace_symbols", "security_scan",
		"diff", "usage", "history", "context_pack",
		"memory_retain", "memory_recall", "memory_forget", "memory_reflect",
		"memory_learn", "tool_search",
	}
	cases := []struct {
		name string
		reg  *tools.Registry
	}{
		{"New", tools.New(t.TempDir(), true)},
		{"NewWithLimit", tools.NewWithLimit(t.TempDir(), true, 1000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := specNames(tc.reg.Specs()); !reflect.DeepEqual(got, golden) {
				t.Fatalf("Specs() order = %v, want %v", got, golden)
			}
			if got := tc.reg.Names(); !reflect.DeepEqual(got, golden) {
				t.Fatalf("Names() order = %v, want %v", got, golden)
			}
		})
	}
}
