package toolrank

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTokenizeCamelCaseAndPunctuation(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"webFetch", []string{"web", "fetch"}},
		{"WebFetchAPI", []string{"web", "fetch", "api"}},
		{"web_fetch2", []string{"web", "fetch", "2"}},
		{"HTTPServer", []string{"http", "server"}},
		{"the quick brown fox", []string{"quick", "brown", "fox"}},
		{"", nil},
	}
	for _, tc := range cases {
		if got := Tokenize(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTokenizePluralNormalization(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"requests", []string{"request"}},
		{"matches", []string{"match"}},
		{"boxes", []string{"box"}},
		{"status", []string{"statu"}}, // 保守归一：查询与文档同样归一，匹配仍成立
		{"class", []string{"class"}},  // ss 结尾不去 s
	}
	for _, tc := range cases {
		if got := Tokenize(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTokenizeCJKProducesSingleAndBigram(t *testing.T) {
	got := Tokenize("获取网页")
	want := []string{"获", "取", "网", "页", "获取", "取网", "网页"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize(CJK) = %v, want %v", got, want)
	}
}

func TestRankRareTermAndCoverage(t *testing.T) {
	docs := []Doc{
		{Name: "web_fetch", Desc: "Fetch a URL from the internet", Props: []string{"url", "method"}},
		{Name: "web_search", Desc: "Search the web via IANA registry", Props: []string{"query"}},
		{Name: "current_time", Desc: "Show current time in IANA timezones", Props: []string{"zone"}},
	}

	// "web fetch" 应命中 web_fetch 且排在 web_search 之前（后者只命中 web）。
	got := Rank("web fetch", docs)
	if len(got) == 0 || got[0].Doc.Name != "web_fetch" {
		t.Fatalf("Rank(web fetch) = %+v, want web_fetch first", got)
	}
	for _, s := range got {
		if s.Doc.Name == "web_search" {
			t.Fatal("web_search must not match a query requiring 'fetch'")
		}
	}

	// 稀有词 IANA 只出现在两个 description 中，应同时命中且当前时间排前或并列。
	iana := Rank("IANA", docs)
	if len(iana) != 2 {
		t.Fatalf("Rank(IANA) = %+v, want 2 matches", iana)
	}

	// 覆盖门槛：任一 token 无文档命中即整体无结果。
	if none := Rank("zzz-no-such-tool", docs); len(none) != 0 {
		t.Fatalf("Rank(zzz-no-such-tool) = %+v, want no matches", none)
	}
}

func TestRankCJKQuery(t *testing.T) {
	docs := []Doc{
		{Name: "web_fetch", Desc: "获取网页内容并保存到工作区", Props: []string{"url"}},
		{Name: "ls", Desc: "list directory entries", Props: nil},
	}
	got := Rank("获取网页", docs)
	if len(got) == 0 || got[0].Doc.Name != "web_fetch" {
		t.Fatalf("Rank(CJK) = %+v, want web_fetch", got)
	}
}

func TestIndexSchemaCollectsPropsAndFollowsRef(t *testing.T) {
	clearSchemaCache()
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{"query":{"type":"string"},"opts":{"$ref":"#/$defs/Opts"}},
		"$defs":{"Opts":{"type":"object","properties":{"limit":{"type":"integer"},"cursor":{"type":"string"}}}}
	}`)
	got := IndexSchema(raw)
	want := []string{"cursor", "limit", "opts", "query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IndexSchema = %v, want %v", got, want)
	}

	// 缓存命中：相同文本返回同一份结果（内容相等）。
	if again := IndexSchema(raw); !reflect.DeepEqual(again, want) {
		t.Fatalf("cached IndexSchema = %v, want %v", again, want)
	}
}

func TestIndexSchemaRejectsInvalidAndCyclic(t *testing.T) {
	if got := IndexSchema(json.RawMessage(`{`)); got != nil {
		t.Fatalf("invalid schema = %v, want nil", got)
	}
	if got := IndexSchema(nil); got != nil {
		t.Fatalf("nil schema = %v, want nil", got)
	}
	raw := json.RawMessage(`{"properties":{"a":{"$ref":"#/$defs/A"}},"$defs":{"A":{"properties":{"self":{"$ref":"#/$defs/A"}}}}}`)
	_ = IndexSchema(raw) // 不因自引用 while 循环卡死
}

func BenchmarkRank(b *testing.B) {
	docs := []Doc{
		{Name: "web_fetch", Desc: "Fetch a URL from the internet", Props: []string{"url", "method"}},
		{Name: "web_search", Desc: "Search the web", Props: []string{"query"}},
		{Name: "current_time", Desc: "Show current time in IANA timezones", Props: []string{"zone"}},
	}
	for i := 0; i < b.N; i++ {
		_ = Rank("web fetch", docs)
	}
}
