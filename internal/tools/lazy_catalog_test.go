package tools

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func spec(names ...string) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(names))
	for _, n := range names {
		var s llm.ToolSpec
		s.Function.Name = n
		out = append(out, s)
	}
	return out
}

func specNamesOf(specs []llm.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Function.Name)
	}
	return out
}

func hasName(specs []llm.ToolSpec, name string) bool {
	for _, s := range specs {
		if s.Function.Name == name {
			return true
		}
	}
	return false
}

// TestLazyFilterSpecsDefault 验证零配置（默认）即精简：核心工具 + tool_search
// 保留，其余工具隐藏。用户无需做任何选择。
func TestLazyFilterSpecsDefault(t *testing.T) {
	t.Setenv(fullToolsEnv, "")
	in := spec("read", "write", "edit", "bash", "ls", "grep", "find", "glob",
		"todo", "multi_edit", "tool_search", "web_search", "memory_recall",
		"lsp_symbols", "current_time", "repo_map")
	got := LazyFilterSpecs(in)
	for _, core := range []string{"read", "write", "edit", "bash", "tool_search"} {
		if !hasName(got, core) {
			t.Errorf("核心工具 %q 应保留，得到 %v", core, specNamesOf(got))
		}
	}
	for _, hidden := range []string{"web_search", "memory_recall", "lsp_symbols", "current_time", "repo_map"} {
		if hasName(got, hidden) {
			t.Errorf("工具 %q 应默认隐藏（模型可经 tool_search 发现），got %v", hidden, specNamesOf(got))
		}
	}
	if len(got) != len(lazyCoreTools) {
		t.Errorf("保留数 = %d, want %d: %v", len(got), len(lazyCoreTools), specNamesOf(got))
	}
}

// TestLazyFilterSpecsFullEscapeHatch 验证逃生舱 ARGENT_FULL_TOOLS 才恢复全部工具，
// 且真值大小写/空白不敏感、假值不触发。
func TestLazyFilterSpecsFullEscapeHatch(t *testing.T) {
	in := spec("read", "web_search", "memory_recall")
	for _, truthy := range []string{"1", "true", "YES", " on "} {
		t.Run("truthy="+truthy, func(t *testing.T) {
			t.Setenv(fullToolsEnv, truthy)
			got := LazyFilterSpecs(in)
			if len(got) != len(in) {
				t.Fatalf("逃生舱应原样返回 %d 个，得到 %d: %v", len(in), len(got), specNamesOf(got))
			}
		})
	}
	for _, falsy := range []string{"0", "false", "no", "off", "maybe"} {
		t.Setenv(fullToolsEnv, falsy)
		if got := LazyFilterSpecs(in); len(got) == len(in) {
			t.Errorf("假值 %q 不应关闭精简，得到 %v", falsy, specNamesOf(got))
		}
	}
}

// TestLazyFilterSpecsNoDuplicates 验证过滤不重复、不丢失核心工具顺序。
func TestLazyFilterSpecsNoDuplicates(t *testing.T) {
	t.Setenv(fullToolsEnv, "")
	in := spec("read", "write", "web_search", "edit", "tool_search", "bash")
	got := LazyFilterSpecs(in)
	want := []string{"read", "write", "edit", "tool_search", "bash"}
	if gotNames := specNamesOf(got); len(gotNames) != len(want) {
		t.Fatalf("got %v, want %v", gotNames, want)
	}
	for i, name := range want {
		if got[i].Function.Name != name {
			t.Fatalf("位置 %d: got %q, want %q", i, got[i].Function.Name, name)
		}
	}
}
