package tools

import (
	"context"
	"strings"
	"testing"

	"example.com/argentgo/internal/codegraph"
)

func TestRegisterCodeGraphNotInDefaultGolden(t *testing.T) {
	r := New(t.TempDir(), true)

	// 默认注册表不含 codegraph_query（golden 不变）。
	for _, name := range r.Names() {
		if name == codegraphToolName {
			t.Fatal("默认注册表不应包含 codegraph_query")
		}
	}
	before := r.Names()

	RegisterCodeGraph(r)
	once := r.Names()
	if len(once) != len(before)+1 {
		t.Fatalf("注册后数量 = %d，期望 %d", len(once), len(before)+1)
	}
	if once[len(once)-1] != codegraphToolName {
		t.Fatalf("codegraph_query 应追加在末尾，实际末尾 %q", once[len(once)-1])
	}
	for i := range before {
		if once[i] != before[i] {
			t.Fatalf("既有顺序被改变：第 %d 项 %q != %q", i, once[i], before[i])
		}
	}

	// 幂等：重复注册不再插入。
	RegisterCodeGraph(r)
	if got := r.Names(); len(got) != len(once) {
		t.Fatalf("重复注册后数量 = %d，期望 %d", len(got), len(once))
	}

	// nil 是空操作。
	RegisterCodeGraph(nil)

	// Specs 也应包含该工具，且 schema 带意图字段（Specs 兜底注入）。
	found := false
	for _, spec := range r.Specs() {
		if spec.Function.Name == codegraphToolName {
			found = true
			if !strings.Contains(string(spec.Function.Parameters), `"i"`) {
				t.Fatal("Specs 中的 codegraph_query schema 应带 i 意图字段")
			}
		}
	}
	if !found {
		t.Fatal("Specs 应包含 codegraph_query")
	}
}

func TestSetCodeGraphInjection(t *testing.T) {
	r := New(t.TempDir(), true)
	RegisterCodeGraph(r)
	fake := &fakeCodeGraph{
		result: codegraph.Result{
			Definitions: []codegraph.Edge{
				codegraphTestEdge("def", "high", "pkg", "a.go", "func Foo() {}", 1, 1),
			},
		},
		stats: codegraph.Stats{Files: 1, Symbols: 1, Edges: 1},
	}
	r.SetCodeGraph(fake)

	// 注入后原位置可用，输出带证据头与逐边证据。
	out, err := r.Run(context.Background(), codegraphToolName, `{"name":"Foo"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[tool:codegraph_query") {
		t.Fatalf("注入后查询应带证据头：\n%s", out)
	}
	if !strings.Contains(out, "DEF a.go:1:1 [rule=def conf=high]") {
		t.Fatalf("注入后查询应含逐边证据：\n%s", out)
	}
	if fake.queryCalls != 1 {
		t.Fatalf("Query 调用次数 = %d，期望 1", fake.queryCalls)
	}

	// 注入不改变注册顺序与数量。
	if got := r.Names(); got[len(got)-1] != codegraphToolName || len(got) != len(New(t.TempDir(), true).Names())+1 {
		t.Fatalf("注入不应改变注册顺序/数量：%v", got)
	}
}

func TestSetCodeGraphRegistersWhenAbsent(t *testing.T) {
	r := New(t.TempDir(), true)
	r.SetCodeGraph(&fakeCodeGraph{})
	found := false
	for _, name := range r.Names() {
		if name == codegraphToolName {
			found = true
		}
	}
	if !found {
		t.Fatal("SetCodeGraph 在未注册时应自动完成注册")
	}
}

func TestSetCodeGraphNilIsIdempotent(t *testing.T) {
	r := New(t.TempDir(), true)
	r.SetCodeGraph(nil)
	RegisterCodeGraph(r)
	out, err := r.Run(context.Background(), codegraphToolName, `{"name":"Foo"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out != codegraphUnavailable {
		t.Fatalf("nil 适配器应报告不可用，实际 %q", out)
	}
}
