package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"example.com/argentgo/internal/pluginapi"
)

// TestBuiltinRegistryStableOrder 验证实例化顺序由显式 order 决定，
// 即使注册调用以相反顺序发生，输出的命令名列表也保持不变。
func TestBuiltinRegistryStableOrder(t *testing.T) {
	var reg pluginapi.Registry[Command]
	// 故意逆序注册：若顺序取决于注册先后，这里会得到 review, context。
	reg.MustRegister("review", builtinOrderReview, ReviewCommand)
	reg.MustRegister("context", builtinOrderContext, ContextCommand)

	if got, want := reg.Names(), []string{"context", "review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("乱序注册后的名字列表 = %v, 期望 %v", got, want)
	}
	factories := reg.Factories()
	if len(factories) != 2 {
		t.Fatalf("构造器数量 = %d, 期望 2", len(factories))
	}
	if got := factories[0]().Name; got != "context" {
		t.Fatalf("首个构造器产出 %q, 期望 %q", got, "context")
	}
	if got := factories[1]().Name; got != "review" {
		t.Fatalf("次个构造器产出 %q, 期望 %q", got, "review")
	}

	// 包级注册表本身也必须按同一顺序暴露构造器。
	if got, want := builtinRegistry.Names(), []string{"context", "review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("内置注册表名字列表 = %v, 期望 %v", got, want)
	}
}

// TestBuiltinRegistryRejectsDuplicate 验证重名注册会报错且不会覆盖已有条目，
// 使配置错误在启动期暴露。
func TestBuiltinRegistryRejectsDuplicate(t *testing.T) {
	var reg pluginapi.Registry[Command]
	if err := reg.Register("context", builtinOrderContext, ContextCommand); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	err := reg.Register("context", builtinOrderReview, ReviewCommand)
	if err == nil {
		t.Fatal("重复注册未报错")
	}
	if got := reg.Len(); got != 1 {
		t.Fatalf("重复注册后条目数 = %d, 期望 1", got)
	}
	if got := reg.Names()[0]; got != "context" {
		t.Fatalf("重复注册后保留的名字 = %q, 期望 %q", got, "context")
	}
}

// TestNewSetWithBuiltinsMatchesLegacyOutput 固定期望的命令名列表与完整描述符，
// 保证改为注册表实例化后与旧行为逐字节一致（golden/eval 快照依赖此顺序）。
func TestNewSetWithBuiltinsMatchesLegacyOutput(t *testing.T) {
	set := NewSetWithBuiltins(nil)
	got := make([]string, 0, len(set.commands))
	for _, cmd := range set.commands {
		got = append(got, cmd.Name)
	}
	if want := []string{"context", "review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("内置命令名列表 = %v, 期望 %v", got, want)
	}

	checks := []struct {
		name        string
		description string
		body        string
		path        string
	}{
		{
			name:        "context",
			description: "Show a read-only view of what is in the model context without modifying anything",
			body:        "Report the active memory documents, skills, commands, tools, sandbox/approval mode, .argentignore status and an estimated context size. Read-only: do not modify files.",
			path:        "<builtin:context>",
		},
		{
			name:        "review",
			description: "Read-only review of the dirty git tree without modifying it",
			body:        "Review the working tree diff: report TODO/FIXME markers, debug prints, large diffs and secret-looking literals. Do not modify files.",
			path:        "<builtin:review>",
		},
	}
	for _, check := range checks {
		cmd, ok := set.Get(check.name)
		if !ok {
			t.Fatalf("未找到内置命令 %q", check.name)
		}
		if cmd.Description != check.description || cmd.Body != check.body || cmd.Path != check.path {
			t.Errorf("命令 %q 描述符不匹配: description=%q body=%q path=%q",
				check.name, cmd.Description, cmd.Body, cmd.Path)
		}
	}
}

// TestNewSetWithBuiltinsKeepsDiscoveredCommandPriority 验证显式 markdown 命令仍优先于
// 同名内置命令（Add 从不覆盖已有条目），与旧构造路径一致。
func TestNewSetWithBuiltinsKeepsDiscoveredCommandPriority(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ProjectSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建命令目录: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.md"), []byte("---\ndescription: Project override\n---\nCustom body."), 0o644); err != nil {
		t.Fatalf("写入命令文件: %v", err)
	}

	set := NewSetWithBuiltins([]Source{{Root: dir}})
	cmd, ok := set.Get("context")
	if !ok {
		t.Fatal("未找到 context 命令")
	}
	if cmd.Description != "Project override" || cmd.Body != "Custom body." || cmd.Path == "<builtin:context>" {
		t.Fatalf("内置命令覆盖了显式命令: %+v", cmd)
	}
	if _, ok := set.Get("review"); !ok {
		t.Fatal("review 内置命令丢失")
	}
}
