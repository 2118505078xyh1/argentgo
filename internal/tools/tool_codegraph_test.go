package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/codegraph"
)

// fakeCodeGraph 是 CodeGraph 的测试替身，记录最后一次 Query 参数。
type fakeCodeGraph struct {
	result     codegraph.Result
	stats      codegraph.Stats
	gotQuery   codegraph.Query
	queryCalls int
}

func (f *fakeCodeGraph) Query(q codegraph.Query) codegraph.Result {
	f.gotQuery = q
	f.queryCalls++
	return f.result
}

func (f *fakeCodeGraph) Stats() codegraph.Stats { return f.stats }

func codegraphTestEdge(rule, conf, enclosing, file, snippet string, line, col int) codegraph.Edge {
	return codegraph.Edge{
		From:      codegraph.Symbol{Name: "Foo", File: file},
		To:        codegraph.Ref{Name: "Bar"},
		Rule:      rule,
		Conf:      conf,
		Enclosing: enclosing,
		At:        codegraph.Pos{File: file, Line: line, Col: col},
		Snippet:   snippet,
	}
}

func TestCodeGraphToolSchemaAndIdentity(t *testing.T) {
	tool := codegraphTool{}
	if tool.Name() != "codegraph_query" {
		t.Fatalf("Name = %q", tool.Name())
	}
	if tool.Mutating() {
		t.Fatal("codegraph_query 应为只读")
	}
	if !strings.Contains(tool.Description(), "索引") {
		t.Fatalf("描述应为中文：%q", tool.Description())
	}
	schema := tool.Schema()
	for _, field := range []string{`"name"`, `"pkg"`, `"receiver"`, `"direction"`, `"limit"`, `"required":["name"]`} {
		if !strings.Contains(schema, field) {
			t.Errorf("schema 缺少字段 %s：%s", field, schema)
		}
	}
}

func TestCodeGraphToolQueryOutputHasEvidence(t *testing.T) {
	fake := &fakeCodeGraph{
		result: codegraph.Result{
			IndexedFiles: 3,
			TotalSymbols: 12,
			Ambiguous:    1,
			Definitions: []codegraph.Edge{
				codegraphTestEdge("def", "high", "pkg", "a.go", "func Foo() {}", 10, 2),
			},
			Edges: []codegraph.Edge{
				codegraphTestEdge("call", "med", "Bar", "b.go", "Foo()", 20, 5),
			},
			Rules: []string{"call-graph", "type-ref"},
		},
		stats: codegraph.Stats{Files: 3, Symbols: 12, Edges: 9},
	}
	out, err := (codegraphTool{cg: fake}).Run(context.Background(), t.TempDir(),
		json.RawMessage(`{"name":"Foo","direction":"callers","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}

	// 证据头 + 汇总行 + 逐边证据 + Rules 解释。
	for _, want := range []string{
		"[tool:codegraph_query",
		"索引文件=3 符号=12 边=9 命中=2 歧义=1",
		"DEF a.go:10:2 [rule=def conf=high] enclosing=pkg: func Foo() {}",
		"CALL b.go:20:5 [rule=call conf=med] enclosing=Bar: Foo()",
		"规则: call-graph, type-ref",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
	}

	// Query 参数应被正确传递。
	if fake.gotQuery.Name != "Foo" || fake.gotQuery.Direction != "callers" || fake.gotQuery.Limit != 5 {
		t.Fatalf("Query 参数 = %+v", fake.gotQuery)
	}
}

func TestCodeGraphToolCJKPathToSlash(t *testing.T) {
	cjkPath := filepath.Join("内部", "模块", "文件.go")
	fake := &fakeCodeGraph{
		result: codegraph.Result{
			Definitions: []codegraph.Edge{
				codegraphTestEdge("def", "high", "包", cjkPath, "中文内容", 1, 1),
			},
		},
	}
	out, err := (codegraphTool{cg: fake}).Run(context.Background(), t.TempDir(),
		json.RawMessage(`{"name":"中文"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "内部/模块/文件.go") {
		t.Fatalf("CJK 路径应转为 / 分隔：\n%s", out)
	}
	if strings.Contains(out, `\`) {
		t.Fatalf("输出不应含反斜杠：\n%s", out)
	}
	if !utf8.ValidString(out) {
		t.Fatal("输出应为合法 UTF-8")
	}
}

func TestCodeGraphToolLimitTruncation(t *testing.T) {
	edges := make([]codegraph.Edge, 5)
	for i := range edges {
		edges[i] = codegraphTestEdge("call", "c", "e", "a.go", "s", i+1, 1)
	}
	fake := &fakeCodeGraph{result: codegraph.Result{Edges: edges}}
	out, err := (codegraphTool{cg: fake}).Run(context.Background(), t.TempDir(),
		json.RawMessage(`{"name":"Foo","limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "CALL "); got != 2 {
		t.Fatalf("应只渲染 2 条边，实际 %d：\n%s", got, out)
	}
	if !strings.Contains(out, "[3 more omitted by limit=2]") {
		t.Fatalf("缺少截断提示：\n%s", out)
	}
	if !strings.Contains(out, "clipped") {
		t.Fatalf("来源头应标记 clipped：\n%s", out)
	}
}

func TestCodeGraphToolLimitDefaultsAndCap(t *testing.T) {
	fake := &fakeCodeGraph{}
	tool := codegraphTool{cg: fake}
	if _, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{"name":"Foo"}`)); err != nil {
		t.Fatal(err)
	}
	if fake.gotQuery.Limit != codegraphDefaultLimit {
		t.Fatalf("默认 limit = %d，期望 %d", fake.gotQuery.Limit, codegraphDefaultLimit)
	}
	if fake.gotQuery.Direction != "all" {
		t.Fatalf("默认 direction = %q，期望 all", fake.gotQuery.Direction)
	}

	capped := &fakeCodeGraph{}
	if _, err := (codegraphTool{cg: capped}).Run(context.Background(), t.TempDir(),
		json.RawMessage(`{"name":"Foo","limit":999}`)); err != nil {
		t.Fatal(err)
	}
	if capped.gotQuery.Limit != codegraphMaxLimit {
		t.Fatalf("封顶 limit = %d，期望 %d", capped.gotQuery.Limit, codegraphMaxLimit)
	}
}

func TestCodeGraphToolEmptyResultAndUseless(t *testing.T) {
	tool := codegraphTool{cg: &fakeCodeGraph{}}
	out, err := tool.Run(context.Background(), t.TempDir(), json.RawMessage(`{"name":"Absent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, codegraphNoMatches) {
		t.Fatalf("空结果应为 (no matches)：\n%s", out)
	}
	if !strings.Contains(out, "[tool:codegraph_query") {
		t.Fatalf("空结果仍应带来源头：\n%s", out)
	}
	if !tool.Meta(nil, out, nil).Useless {
		t.Fatal("空结果应标记 Useless")
	}
	if tool.Meta(nil, out+"\nDEF a.go:1:1", nil).Useless {
		t.Fatal("非空结果不应标记 Useless")
	}
}

func TestCodeGraphToolUnavailable(t *testing.T) {
	out, err := (codegraphTool{}).Run(context.Background(), t.TempDir(),
		json.RawMessage(`{"name":"Foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != codegraphUnavailable {
		t.Fatalf("未注入时应返回 %q，实际 %q", codegraphUnavailable, out)
	}
}

func TestCodeGraphSnippetSanitizedAndClipped(t *testing.T) {
	longSnippet := strings.Repeat("字", codegraphMaxSnippetRunes+10)
	fake := &fakeCodeGraph{
		result: codegraph.Result{
			Definitions: []codegraph.Edge{
				codegraphTestEdge("def\nrule", "high", "包\t名", "a.go", "行1\n"+longSnippet, 1, 1),
			},
		},
	}
	out, err := (codegraphTool{cg: fake}).Run(context.Background(), t.TempDir(),
		json.RawMessage(`{"name":"Foo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n行1") {
		t.Fatalf("片段换行应被清理为空格：\n%s", out)
	}
	if !strings.Contains(out, "rule=def rule") {
		t.Fatalf("rule 控制字符应清理为空格：\n%s", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("超长片段应以省略号截断：\n%s", out)
	}
	if !strings.Contains(out, "sanitized") || !strings.Contains(out, "clipped") {
		t.Fatalf("来源头应标记 sanitized 与 clipped：\n%s", out)
	}
	if !utf8.ValidString(out) {
		t.Fatal("截断后仍应为合法 UTF-8")
	}
}
