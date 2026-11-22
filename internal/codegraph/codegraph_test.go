package codegraph

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeGo 在 dir 下写一个 Go 源文件（按需建目录）。
func writeGo(t *testing.T, dir, rel, src string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// totalEdges 返回结果三类边的总数。
func totalEdges(r Result) int {
	return len(r.Definitions) + len(r.Edges) + len(r.Candidates)
}

func TestBuildStatsOnThisPackage(t *testing.T) {
	ix, err := Build(".")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	st := ix.Stats()
	if st.Files < 3 || st.Symbols < 10 || st.Edges < 10 {
		t.Fatalf("Stats 不合理: %+v", st)
	}
	res := ix.Query(Query{Direction: "all"})
	if res.IndexedFiles != st.Files || res.TotalSymbols != st.Symbols {
		t.Fatalf("Query 计数不一致: files=%d symbols=%d, stats=%+v", res.IndexedFiles, res.TotalSymbols, st)
	}
}

func TestQueryDefinitionsCarryEvidence(t *testing.T) {
	ix, err := Build(".")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res := ix.Query(Query{Name: "Build", Direction: "definitions"})
	if len(res.Definitions) == 0 {
		t.Fatal("应至少有一个 Build 定义")
	}
	var sawFunc bool
	for _, e := range res.Definitions {
		if e.Rule != "definition" || e.Conf != "exact" {
			t.Fatalf("定义边 Rule/Conf 错: %+v", e)
		}
		if e.At.File == "" || e.At.Line <= 0 || e.Snippet == "" {
			t.Fatalf("定义边证据不完整: %+v", e)
		}
		if strings.HasPrefix(e.Snippet, "func Build(") || strings.HasPrefix(e.Snippet, "func BuildIncremental(") {
			sawFunc = true
		}
	}
	if !sawFunc {
		t.Fatalf("未找到 Build 函数定义的源码证据: %+v", res.Definitions)
	}
}

func TestAllEdgesCarryExplanation(t *testing.T) {
	ix, err := Build(".")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	known := map[string]bool{
		"call": true, "method_call": true, "type_ref": true,
		"composite": true, "field": true,
	}
	for _, e := range ix.edges {
		if !known[e.Rule] {
			t.Fatalf("未知 Rule %q: %+v", e.Rule, e)
		}
		if e.Conf != "exact" && e.Conf != "candidate" {
			t.Fatalf("未知 Conf %q: %+v", e.Conf, e)
		}
		if e.At.File == "" || e.At.Line <= 0 || e.Snippet == "" {
			t.Fatalf("边缺少可解释来源: %+v", e)
		}
		if len(e.Snippet) > maxSnippetBytes || !utf8.ValidString(e.Snippet) {
			t.Fatalf("Snippet 非 CJK 安全/超限: %q", e.Snippet)
		}
	}
}

func TestQueryExactSymbolReturnsDefinitionsAndRefs(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "a.go", `package fixture

type Engine struct{}

func Helper() {}
func Boot()    { Helper() }
`)

	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res := ix.Query(Query{Name: "Helper", Pkg: "fixture", Direction: "all"})
	if len(res.Definitions) != 1 || len(res.Edges) != 1 || len(res.Candidates) != 0 {
		t.Fatalf("精确符号应返回 1 定义 + 1 exact 边: %+v", res)
	}
	e := res.Edges[0]
	if e.Rule != "call" || e.Conf != "exact" || e.Enclosing != "Boot" {
		t.Fatalf("引用边元信息错误: %+v", e)
	}
	if e.To.Name != "Helper" || e.To.Pkg != "fixture" || e.At.File != "a.go" || e.At.Line <= 0 {
		t.Fatalf("引用边目标/位置错误: %+v", e)
	}
	if !strings.Contains(e.Snippet, "Helper()") {
		t.Fatalf("Snippet 未包含触发语句: %q", e.Snippet)
	}
	if !reflect.DeepEqual(res.Rules, []string{"call", "definition"}) {
		t.Fatalf("Rules 错误: %v", res.Rules)
	}
}

func TestQueryRunReturnsCandidatesAndAmbiguityTiers(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "run.go", `package fixture

type A struct{}
type B struct{}

func (a *A) Run() {}
func (b *B) Run() {}

// 局部变量 x 不在接收者变量名表中，因此裸名 Run 命中 2 个定义，应判为 candidate。
func UseA(x *A) { x.Run() }

// a 是 A 的接收者变量名，带类型限定的 Run 应唯一命中。
func (a *A) Fire() { a.Run() }
`)

	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	amb := ix.Query(Query{Name: "Run", Direction: "all"})
	if len(amb.Definitions) != 2 {
		t.Fatalf("应有 2 个 Run 定义: %+v", amb.Definitions)
	}
	if len(amb.Candidates) == 0 || amb.Ambiguous == 0 {
		t.Fatalf("Run 应产生 candidate 与歧义计数: %+v", amb)
	}
	for _, e := range amb.Candidates {
		if e.Conf != "candidate" || e.To.Name != "Run" {
			t.Fatalf("candidate 分档错误: %+v", e)
		}
	}

	exact := ix.Query(Query{Name: "Run", Receiver: "A", Direction: "all"})
	if len(exact.Definitions) != 1 {
		t.Fatalf("带 Receiver=A 应只剩 1 个定义: %+v", exact.Definitions)
	}
	if len(exact.Edges) != 1 || exact.Edges[0].Conf != "exact" || exact.Edges[0].Rule != "method_call" {
		t.Fatalf("带 Receiver=A 的引用应唯一命中: %+v", exact.Edges)
	}
	if exact.Ambiguous != 0 {
		t.Fatalf("精确查询不应有歧义: %d", exact.Ambiguous)
	}
}

// 用本仓真实代码做 fixture：internal/tools 有大量同名 Run 方法，
// 正好覆盖"高频短名歧义"场景，无需联网。
func TestQueryRunOnRealToolsFixtures(t *testing.T) {
	ix, err := Build("../tools")
	if err != nil {
		t.Fatalf("Build ../tools: %v", err)
	}
	res := ix.Query(Query{Name: "Run", Direction: "all"})
	if len(res.Definitions) < 5 {
		t.Fatalf("应有多个真实 Run 定义: %d", len(res.Definitions))
	}
	if len(res.Candidates) == 0 || res.Ambiguous == 0 {
		t.Fatalf("高频短名 Run 应产生 candidate 与歧义计数: defs=%d exact=%d cand=%d amb=%d",
			len(res.Definitions), len(res.Edges), len(res.Candidates), res.Ambiguous)
	}
	for _, e := range res.Candidates {
		// all 模式同时含入边与出边，故名字可能出现在 To 或 From 任一侧。
		if e.To.Name != "Run" && e.From.Name != "Run" {
			t.Fatalf("candidate 与查询无关: %+v", e)
		}
		if e.Conf != "candidate" || e.At.File == "" || e.Snippet == "" || e.Rule == "" {
			t.Fatalf("真实 candidate 证据不完整: %+v", e)
		}
	}
}

func TestDirectionFourStates(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "flow.go", `package fixture

func Alpha() { Beta() }
func Beta()  { Gamma() }
func Gamma() {}
`)

	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, tc := range []struct {
		dirn                   string
		defs, exact, candidate int
	}{
		{"definitions", 1, 0, 0},
		{"callers", 0, 0, 0},
		{"references", 0, 1, 0},
		{"all", 1, 1, 0},
	} {
		res := ix.Query(Query{Name: "Alpha", Direction: tc.dirn})
		if len(res.Definitions) != tc.defs || len(res.Edges) != tc.exact || len(res.Candidates) != tc.candidate {
			t.Fatalf("Direction=%s 结果错误: defs=%d exact=%d cand=%d, want %d/%d/%d",
				tc.dirn, len(res.Definitions), len(res.Edges), len(res.Candidates), tc.defs, tc.exact, tc.candidate)
		}
	}
	callers := ix.Query(Query{Name: "Beta", Direction: "callers"})
	if len(callers.Edges) != 1 || callers.Edges[0].From.Name != "Alpha" {
		t.Fatalf("Beta 的 callers 应来自 Alpha: %+v", callers.Edges)
	}
}

func TestLimitTruncation(t *testing.T) {
	ix, err := Build(".")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	all := ix.Query(Query{Direction: "all"})
	capped := ix.Query(Query{Direction: "all", Limit: 3})
	if totalEdges(capped) != 3 || capped.Truncated != 1 {
		t.Fatalf("Limit 应为 3 且截断: total=%d truncated=%d", totalEdges(capped), capped.Truncated)
	}
	if len(capped.Definitions) != 3 {
		t.Fatalf("Limit 应先分配给定定义: %+v", capped)
	}
	if reflect.DeepEqual(capped.Definitions, all.Definitions[:3]) == false {
		t.Fatal("截断结果应是全量结果的前缀")
	}
	roomy := ix.Query(Query{Direction: "all", Limit: totalEdges(all) + 10})
	if roomy.Truncated != 0 || totalEdges(roomy) != totalEdges(all) {
		t.Fatalf("额度充足时不应截断: %+v", roomy)
	}
}

func TestDeterministicBuild(t *testing.T) {
	ix1, err := Build(".")
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	ix2, err := Build(".")
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	if !reflect.DeepEqual(ix1.Stats(), ix2.Stats()) {
		t.Fatalf("Stats 不一致: %+v vs %+v", ix1.Stats(), ix2.Stats())
	}
	for _, q := range []Query{
		{Name: "Build", Direction: "all"},
		{Name: "", Direction: "all"},
		{Name: "Rule", Direction: "callers", Limit: 5},
	} {
		if !reflect.DeepEqual(ix1.Query(q), ix2.Query(q)) {
			t.Fatalf("Query %+v 结果不一致", q)
		}
	}
}

func TestMissingSymbolAndEmptyDir(t *testing.T) {
	ix, err := Build(".")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	miss := ix.Query(Query{Name: "ZZZ_NoSuchSymbol_ZZZ", Direction: "all"})
	if totalEdges(miss) != 0 || len(miss.Rules) != 0 || miss.Ambiguous != 0 {
		t.Fatalf("不存在的符号应返回空结果: %+v", miss)
	}

	empty, err := Build(t.TempDir())
	if err != nil {
		t.Fatalf("Build 空目录: %v", err)
	}
	if st := empty.Stats(); st != (Stats{}) {
		t.Fatalf("空目录 Stats 应为零: %+v", st)
	}
	if r := empty.Query(Query{Direction: "all"}); totalEdges(r) != 0 {
		t.Fatalf("空目录查询应为空: %+v", r)
	}
}

func TestCJKPathsAndRuneSafeSnippet(t *testing.T) {
	dir := t.TempDir()
	longCJK := strings.Repeat("中文注释", 40) // 480 字节
	writeGo(t, dir, "中文/图谱.go", `package fixture

func Helper() {}
func Long() { /* `+longCJK+` */ Helper() }
`)

	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res := ix.Query(Query{Name: "Helper", Direction: "callers"})
	if len(res.Edges) != 1 {
		t.Fatalf("应有 1 条调用边: %+v", res)
	}
	e := res.Edges[0]
	if !strings.Contains(e.At.File, "中文/图谱.go") || strings.Contains(e.At.File, "\\") {
		t.Fatalf("CJK 路径必须 slash 化: %q", e.At.File)
	}
	if len(e.Snippet) > maxSnippetBytes || !utf8.ValidString(e.Snippet) || !strings.HasSuffix(e.Snippet, "...") {
		t.Fatalf("CJK 片段必须 rune 安全且截到上限内: %q (%d bytes)", e.Snippet, len(e.Snippet))
	}
	if !utf8.ValidString(e.Snippet) || strings.ContainsRune(e.Snippet, utf8.RuneError) {
		t.Fatalf("片段不能切断多字节字符: %q", e.Snippet)
	}
}

func TestIncrementalBuildReuseAndRefresh(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "a.go", `package fixture

func Alpha() {}
func Boot() { Alpha() }
`)
	ix1, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	ix2, err := BuildIncremental(dir, ix1)
	if err != nil {
		t.Fatalf("BuildIncremental: %v", err)
	}
	if !reflect.DeepEqual(ix1.Stats(), ix2.Stats()) || ix1.files["a.go"] != ix2.files["a.go"] {
		t.Fatal("未变更文件应被复用且统计一致")
	}

	writeGo(t, dir, "a.go", `package fixture

func Alpha() {}
func Beta()  {}
func Boot()  { Alpha(); Beta() }
`)
	ix3, err := BuildIncremental(dir, ix2)
	if err != nil {
		t.Fatalf("BuildIncremental after edit: %v", err)
	}
	if ix3.files["a.go"] == ix2.files["a.go"] {
		t.Fatal("变更文件必须重新解析")
	}
	if len(ix3.Query(Query{Name: "Beta", Direction: "definitions"}).Definitions) != 1 {
		t.Fatal("增量构建应看到新增定义")
	}
}

func TestIgnoredDirectories(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "main.go", `package fixture

func MainFunc() {}
`)
	writeGo(t, dir, "vendor/lib.go", `package lib

func VendorOnly() {}
`)
	writeGo(t, dir, ".git/hooks.go", `package hooks

func GitOnly() {}
`)
	ix, err := Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if st := ix.Stats(); st.Files != 1 {
		t.Fatalf("忽略目录应只留下 1 个文件: %+v", st)
	}
	for _, name := range []string{"VendorOnly", "GitOnly"} {
		if r := ix.Query(Query{Name: name, Direction: "all"}); totalEdges(r) != 0 {
			t.Fatalf("%s 不应被索引: %+v", name, r)
		}
	}
}
