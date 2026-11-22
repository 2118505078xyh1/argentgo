package app

import (
	"reflect"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件守护 mcp.go 中 codegraph 的 opt-in 注册分支：默认关闭时工具列表与 golden
// 逐字节相同；开启时只把 codegraph_query 追加到末尾，既有工具顺序不变。

func TestWireOptInToolsCodeGraphDefaultOffGolden(t *testing.T) {
	root := t.TempDir()
	reg := tools.New(root, false)
	before := append([]string(nil), reg.Names()...)
	specsBefore := append([]string(nil), toolNamesFromSpecs(reg.Specs())...)

	// 空注册表与 nil 注册表都不注册任何工具，列表与 golden 逐字节一致。
	for _, opt := range []*OptInRegistry{nil, {}} {
		wireOptInTools(reg, opt)
		if got := reg.Names(); !reflect.DeepEqual(got, before) {
			t.Fatalf("默认关闭时工具名变化: got %v, want %v", got, before)
		}
		if got := toolNamesFromSpecs(reg.Specs()); !reflect.DeepEqual(got, specsBefore) {
			t.Fatalf("默认关闭时 Specs 变化: got %v, want %v", got, specsBefore)
		}
	}
	// 其它键开启时只注册它们自己的工具，codegraph_query 仍必须缺席。
	other := &OptInRegistry{}
	other.Set(OptInApplyPatch, true)
	other.Set(OptInGrepEx, true)
	wireOptInTools(reg, other)
	for _, name := range reg.Names() {
		if name == "codegraph_query" {
			t.Fatal("codegraph 未 opt-in 时不应注册 codegraph_query")
		}
	}
	wireOptInTools(nil, &OptInRegistry{})
	// 其它键开启后 Specs 中仍不得出现 codegraph_query。
	for _, spec := range reg.Specs() {
		if spec.Function.Name == "codegraph_query" {
			t.Fatal("codegraph 未 opt-in 时 Specs 不应包含 codegraph_query")
		}
	}
	wireOptInTools(reg, nil)
}

func TestWireOptInToolsCodeGraphOnRegistersSpec(t *testing.T) {
	reg := tools.New(t.TempDir(), false)
	before := append([]string(nil), reg.Names()...)
	opt := &OptInRegistry{}
	opt.Set(OptInCodeGraph, true)

	wireOptInTools(reg, opt)
	after := reg.Names()
	if len(after) != len(before)+1 || after[len(after)-1] != "codegraph_query" {
		t.Fatalf("codegraph_query 未追加到末尾: before=%v after=%v", before, after)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("既有工具顺序被改变: %v -> %v", before, after)
		}
	}
	// 幂等：重复接线不再插入。
	wireOptInTools(reg, opt)
	if got := reg.Names(); !reflect.DeepEqual(got, after) {
		t.Fatalf("重复接线产生重复工具: %v -> %v", after, got)
	}
	// Specs 同样可查，且完整 UTF-8 描述不为空。
	found := false
	for _, spec := range reg.Specs() {
		if spec.Function.Name == "codegraph_query" {
			found = true
			if spec.Function.Description == "" {
				t.Fatal("codegraph_query spec 描述为空")
			}
		}
	}
	if !found {
		t.Fatal("Specs 中缺少 codegraph_query")
	}
}

func toolNamesFromSpecs(specs []llm.ToolSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.Function.Name)
	}
	return out
}
