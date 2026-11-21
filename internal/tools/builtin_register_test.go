package tools

import (
	"reflect"
	"testing"
)

// TestBuiltinRegistrationOrder 锁定编译期静态注册表的顺序。
// 顺序一旦漂移，Specs() 与 golden 快照都会逐字节改变。
func TestBuiltinRegistrationOrder(t *testing.T) {
	want := []string{
		"read", "write", "edit", "bash", "ls", "grep", "find", "powershell",
		"glob", "todo", "multi_edit", "web_fetch", "git_status", "current_time",
		"web_search", "repo_map", "submit_plan",
		"lsp_symbols", "lsp_references", "lsp_code_actions", "lsp_rename_preview",
		"lsp_format", "lsp_workspace_symbols", "security_scan",
		"diff", "usage", "history",
		"memory_retain", "memory_recall", "memory_forget",
	}
	if got := builtins.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("builtin registration order = %v, want %v", got, want)
	}
}

// TestNewRegistryExactToolOrder 锁定最终注册表顺序（含 context_pack、
// 高级记忆工具与末尾 tool_search）。这是行为不回归的最强断言。
func TestNewRegistryExactToolOrder(t *testing.T) {
	want := []string{
		"read", "write", "edit", "bash", "ls", "grep", "find", "powershell",
		"glob", "todo", "multi_edit", "web_fetch", "git_status", "current_time",
		"web_search", "repo_map", "submit_plan",
		"lsp_symbols", "lsp_references", "lsp_code_actions", "lsp_rename_preview",
		"lsp_format", "lsp_workspace_symbols", "security_scan",
		"diff", "usage", "history", "context_pack",
		"memory_retain", "memory_recall", "memory_forget", "memory_reflect",
		"memory_learn", "tool_search",
	}
	got := New(t.TempDir(), true).Names()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("New().Names() = %v, want %v", got, want)
	}
}

// TestRegistriesAreIndependent 验证每次 New 都实例化一份独立注册表，
// 注册表改造未引入跨运行的共享可变状态。
func TestRegistriesAreIndependent(t *testing.T) {
	dir := t.TempDir()
	a := New(dir, true)
	b := New(dir, true)
	if !reflect.DeepEqual(a.Names(), b.Names()) {
		t.Fatalf("registries differ: %v vs %v", a.Names(), b.Names())
	}
	delete(a.tools, "read")
	if _, ok := b.tools["read"]; !ok {
		t.Fatal("registries share the same tools map")
	}
}
