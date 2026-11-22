package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/codegraph"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件守护 codegraph 适配器：默认关闭时零副作用；开启时构建索引并注入查询工具；
// 构建失败降级为不可用且只写 warn；重复注入幂等。

// codeGraphTestToolset 构造一个只有一个内置注册表的最小工具集。
func codeGraphTestToolset(t *testing.T, root string) *toolset {
	t.Helper()
	reg := tools.New(root, false)
	return &toolset{base: &multiRootTools{roots: []string{root}, regs: []*tools.Registry{reg}}}
}

// codeGraphReg 构造只控制 codegraph 开关的统一注册表。
func codeGraphReg(on bool) *OptInRegistry {
	reg := &OptInRegistry{}
	if on {
		reg.Set(OptInCodeGraph, true)
	}
	return reg
}

// codeGraphWriteGoFile 在 root 下写一个最小 Go 包，供真实 Build 建图。
func codeGraphWriteGoFile(t *testing.T, root, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("写样例源码: %v", err)
	}
}

// codeGraphNames 返回注册表当前工具名列表。
func codeGraphNames(set *toolset) []string {
	return set.base.regs[0].Names()
}

func TestWireCodeGraphDefaultOffNoSideEffects(t *testing.T) {
	calls := 0
	orig := codeGraphBuildHook
	codeGraphBuildHook = func(root string) (*codegraph.Index, error) {
		calls++
		return codegraph.Build(root)
	}
	defer func() { codeGraphBuildHook = orig }()

	root := t.TempDir()
	codeGraphWriteGoFile(t, root, "package sample\n\nfunc HelloWorld() {}\n")
	set := codeGraphTestToolset(t, root)
	before := append([]string(nil), codeGraphNames(set)...)
	var stderr bytes.Buffer

	// 注册表为 nil、空表、其它键开启都不触发任何构建或注入。
	for _, reg := range []*OptInRegistry{nil, {}, codeGraphReg(false)} {
		wireCodeGraph(cli.IO{Err: &stderr}, root, set, reg)
	}
	if calls != 0 {
		t.Fatalf("默认关闭时 Build 被调用了 %d 次", calls)
	}
	if got := codeGraphNames(set); !reflect.DeepEqual(got, before) {
		t.Fatalf("默认关闭时工具列表变化: got %v, want %v", got, before)
	}
	if stderr.Len() != 0 {
		t.Fatalf("默认关闭时不应写 stderr: %q", stderr.String())
	}
	// 系统提示也不应追加（纯函数复核）。
	if codeGraphHintEnabled(nil) {
		t.Fatal("默认关闭时 codeGraphHintEnabled 应为 false")
	}
}

func TestWireCodeGraphOnInjectsQueryTool(t *testing.T) {
	root := t.TempDir()
	codeGraphWriteGoFile(t, root, "package sample\n\nfunc HelloWorld() {}\n")
	set := codeGraphTestToolset(t, root)
	var stderr bytes.Buffer
	env := func(string) string { return "codegraph" }

	// 复刻生产装配：先按 opt-in 注册表登记 spec，再构建并注入索引。
	reg := codeGraphReg(true)
	wireOptInTools(set.base.regs[0], reg)
	before := append([]string(nil), codeGraphNames(set)...)

	wireCodeGraph(cli.IO{Env: env, Err: &stderr}, root, set, reg)
	if stderr.Len() != 0 {
		t.Fatalf("构建成功不应写 warn: %q", stderr.String())
	}
	after := codeGraphNames(set)
	if len(after) != len(before) || after[len(after)-1] != "codegraph_query" {
		t.Fatalf("codegraph_query 未注入: before=%v after=%v", before, after)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("既有工具顺序被改变: %v -> %v", before, after)
		}
	}

	// 工具真的能查到符号，而不是报"不可用"。
	out, err := set.base.regs[0].Run(context.Background(), "codegraph_query", `{"name":"HelloWorld"}`)
	if err != nil {
		t.Fatalf("codegraph_query 执行失败: %v", err)
	}
	if strings.Contains(out, "not available") {
		t.Fatalf("索引未注入，工具仍不可用: %q", out)
	}
	if !strings.Contains(out, "HelloWorld") || !strings.Contains(out, "DEF") {
		t.Fatalf("查询未返回定义证据: %q", out)
	}
}

func TestWireCodeGraphBuildFailureDegrades(t *testing.T) {
	orig := codeGraphBuildHook
	codeGraphBuildHook = func(string) (*codegraph.Index, error) {
		return nil, errors.New("模拟索引构建失败")
	}
	defer func() { codeGraphBuildHook = orig }()

	root := t.TempDir()
	set := codeGraphTestToolset(t, root)
	before := append([]string(nil), codeGraphNames(set)...)
	var stderr bytes.Buffer

	// 必须不崩溃、不注入工具、只在 stderr 留一行 warn。
	wireCodeGraph(cli.IO{Env: func(string) string { return "codegraph" }, Err: &stderr}, root, set, codeGraphReg(true))
	if got := codeGraphNames(set); !reflect.DeepEqual(got, before) {
		t.Fatalf("构建失败时不应注入工具: got %v, want %v", got, before)
	}
	warn := stderr.String()
	if !strings.Contains(warn, "codegraph") || !strings.Contains(warn, "模拟索引构建失败") {
		t.Fatalf("构建失败未写可读 warn: %q", warn)
	}
	if strings.Count(warn, "\n") != 1 {
		t.Fatalf("warn 应恰好一行: %q", warn)
	}
	// nil stderr 也不能 panic。
	wireCodeGraph(cli.IO{Env: func(string) string { return "codegraph" }}, root, set, codeGraphReg(true))
}

func TestWireCodeGraphIdempotentAndNilSafe(t *testing.T) {
	root := t.TempDir()
	codeGraphWriteGoFile(t, root, "package sample\n\nfunc Foo() {}\n")
	set := codeGraphTestToolset(t, root)
	stdio := cli.IO{Env: func(string) string { return "codegraph" }}

	// nil 工具集是空操作：不 panic，也不动真实工具集。
	before := append([]string(nil), codeGraphNames(set)...)
	var nilSet *toolset
	wireCodeGraph(stdio, root, nilSet, codeGraphReg(true))
	if got := codeGraphNames(set); !reflect.DeepEqual(got, before) {
		t.Fatalf("nil 工具集不应产生副作用: %v -> %v", before, got)
	}

	// 重复装配不改变工具数量与顺序。
	wireCodeGraph(stdio, root, set, codeGraphReg(true))
	once := append([]string(nil), codeGraphNames(set)...)
	wireCodeGraph(stdio, root, set, codeGraphReg(true))
	twice := codeGraphNames(set)
	if !reflect.DeepEqual(twice, once) {
		t.Fatalf("重复装配后工具列表变化: %v -> %v", once, twice)
	}
	if len(twice) != len(before)+1 || twice[len(twice)-1] != "codegraph_query" {
		t.Fatalf("重复装配产生了重复工具: %v -> %v", before, twice)
	}
	for i := range before {
		if twice[i] != before[i] {
			t.Fatalf("既有工具顺序被改变: %v -> %v", before, twice)
		}
	}
}

// TestWireCodeGraphConfigOnly 守护阻断修复：仅通过 config 的 optins 块启用
// codegraph 时，提示判定、工具注册与索引注入都必须生效（env 路径完全为空）。
func TestWireCodeGraphConfigOnly(t *testing.T) {
	root := t.TempDir()
	codeGraphWriteGoFile(t, root, "package sample\n\nfunc ConfigOnly() {}\n")
	set := codeGraphTestToolset(t, root)
	env := func(string) string { return "" } // env 中没有任何 codegraph 开关

	// 复刻 app_execute.go 的统一激活面：env OR config。
	optIns := mergeOptIns(ParseOptInEnv(env), map[OptInKey]bool{OptInCodeGraph: true})
	reg := &OptInRegistry{}
	for k, on := range optIns {
		reg.Set(k, on)
	}
	if !codeGraphHintEnabled(reg) {
		t.Fatal("config-only 应启用 codegraph 判定")
	}
	wireOptInTools(set.base.regs[0], reg)

	var stderr bytes.Buffer
	wireCodeGraph(cli.IO{Env: env, Err: &stderr}, root, set, reg)
	if stderr.Len() != 0 {
		t.Fatalf("构建成功不应写 warn: %q", stderr.String())
	}
	out, err := set.base.regs[0].Run(context.Background(), "codegraph_query", `{"name":"ConfigOnly"}`)
	if err != nil {
		t.Fatalf("codegraph_query 执行失败: %v", err)
	}
	if strings.Contains(out, "not available") || !strings.Contains(out, "DEF") || !strings.Contains(out, "ConfigOnly") {
		t.Fatalf("config-only 下索引未注入，工具仍不可用: %q", out)
	}
}
