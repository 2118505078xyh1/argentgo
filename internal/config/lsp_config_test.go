package config

import (
	"reflect"
	"testing"
)

// TestParseLSPConfig 锁定 #43 的配置解析：command/extensions/disabled/env/initialization
// 全量解析，类型错误的字段按缺省处理，七层合并后的 Raw 直接可用。
func TestParseLSPConfig(t *testing.T) {
	raw := map[string]any{
		"lsp": map[string]any{
			"pyright": map[string]any{
				"command":        []any{"pyright-langserver", "--stdio"},
				"extensions":     []any{"py", "pyi"},
				"env":            map[string]any{"KEY": "VALUE"},
				"initialization": map[string]any{"python": map[string]any{"analysis": map[string]any{"autoSearchPaths": true}}},
			},
			"rust-analyzer": map[string]any{"disabled": true},
			"bad":           "not an object",
		},
	}
	cfg := ParseLSPConfig(raw)
	if cfg.Disabled {
		t.Fatal("unexpected global disable")
	}
	pyright, ok := cfg.Servers["pyright"]
	if !ok {
		t.Fatal("pyright missing")
	}
	if !reflect.DeepEqual(pyright.Command, []string{"pyright-langserver", "--stdio"}) {
		t.Fatalf("command = %#v", pyright.Command)
	}
	if !reflect.DeepEqual(pyright.Extensions, []string{"py", "pyi"}) {
		t.Fatalf("extensions = %#v", pyright.Extensions)
	}
	if pyright.Env["KEY"] != "VALUE" {
		t.Fatalf("env = %#v", pyright.Env)
	}
	if pyright.Initialization == nil {
		t.Fatal("initialization missing")
	}
	if !cfg.Servers["rust-analyzer"].Disabled {
		t.Fatal("disabled server not recorded")
	}
	if _, ok := cfg.Servers["bad"]; ok {
		t.Fatal("malformed entry must be ignored")
	}

	// 整体 false 与空块：零值，不产生任何服务器。
	if got := ParseLSPConfig(map[string]any{"lsp": false}); !got.Disabled || len(got.Servers) != 0 {
		t.Fatalf("lsp:false = %+v", got)
	}
	if got := ParseLSPConfig(nil); len(got.Servers) != 0 || got.Disabled {
		t.Fatalf("nil raw = %+v", got)
	}
}

// TestLSPFromProject 锁定来源门控所需的数据：项目级 lsp 块被标记，全局/env 层不会误标。
func TestLSPFromProject(t *testing.T) {
	// 直接构造 Config 验证零值与项目来源的判定语义，避免依赖磁盘布局。
	if (Config{}).LSPFromProject() {
		t.Fatal("zero config must not be project-sourced")
	}
	if !(Config{LSPSource: ValueSourceProject}).LSPFromProject() {
		t.Fatal("project source must be detected")
	}
	if (Config{LSPSource: ValueSourceGlobal}).LSPFromProject() {
		t.Fatal("global source must not be treated as project")
	}
}
