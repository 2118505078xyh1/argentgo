package agent

import (
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// TestSubagentToolTableReuseDefault 锁定 killi 决策 20 的核心不变量：
// 无显式白名单（默认）时，子代理的工具表与父级逐字节一致——这是共享缓存
// 前缀与质量一致的前提。角色白名单刻意保留，不在此列。
func TestSubagentToolTableReuseDefault(t *testing.T) {
	parentSpecs := []llm.ToolSpec{
		toolSpec("read"), toolSpec("write"), toolSpec("bash"), toolSpec("grep"),
	}
	inner := &fakeTools{specs: parentSpecs}
	tt := &taskTools{
		inner:  inner,
		cfg:    &SubagentConfig{},
		parent: Options{System: "parent system"},
	}

	child := tt.childOptions("worker", "")
	if got := child.Tools.Specs(); !reflect.DeepEqual(got, parentSpecs) {
		t.Fatalf("child tool table differs from parent:\n got = %+v\nwant = %+v", got, parentSpecs)
	}
}

// TestSubagentToolTableRespectsRoleWhitelist 验证角色白名单的功能语义不被
// "工具表复用"破坏：显式 AllowedTools 时仍按白名单收窄。
func TestSubagentToolTableRespectsRoleWhitelist(t *testing.T) {
	parentSpecs := []llm.ToolSpec{toolSpec("read"), toolSpec("write"), toolSpec("bash")}
	inner := &fakeTools{specs: parentSpecs}
	tt := &taskTools{
		inner:  inner,
		cfg:    &SubagentConfig{AllowedTools: []string{"read"}},
		parent: Options{System: "parent system"},
	}

	child := tt.childOptions("worker", "")
	got := child.Tools.Specs()
	if len(got) != 1 || got[0].Function.Name != "read" {
		t.Fatalf("role whitelist not respected: %+v", got)
	}
}
