// Package toolrank 为工具发现提供基于 BM25 的相关性排序。
//
// 它只依赖标准库：切词（ASCII camelCase + CJK bigram）、英文停用词/复数归一、
// IDF 与字段加权都在纯函数里完成；schema 属性索引用进程级 sync.Map 缓存。
// 本包不读取文件系统，也不接触注册表——调用方把已经收集好的文本传进来。
package toolrank

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// BM25 参数（与 codex fielded_bm25 对齐）：k1 控制词频饱和，b 控制长度归一。
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// fieldWeights 是字段权重：name > description > schema property。
var fieldWeights = [3]float64{8, 4, 1}

// maxSchemaNodes / maxSchemaDepth 给不可信 schema 的递归收集设上界。
const (
	maxSchemaNodes  = 2048
	maxSchemaDepth  = 24
	maxSchemaRefLen = 256
)

// Doc 是一条可被检索的文本。Props 是 JSON schema 的属性名（可为空）。
type Doc struct {
	Name  string
	Desc  string
	Props []string
}

// Scored 是一条带分数的文档。Score 越大越相关；只返回正分条目。
type Scored struct {
	Doc   Doc
	Score float64
}

// stopwords 是英文填充词：它们单独出现不应产生工具匹配。
// 刻意保持很小，避免把 "fetch" / "search" 这类工具常用动词误杀。
var stopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "can": true, "do": true, "for": true,
	"from": true, "how": true, "i": true, "if": true, "in": true, "into": true,
	"is": true, "it": true, "me": true, "my": true, "no": true, "of": true,
	"on": true, "or": true, "so": true, "such": true, "that": true, "the": true,
	"then": true, "this": true, "to": true, "use": true, "when": true,
	"with": true, "you": true, "your": true,
}

// Tokenize 把文本拆为归一化 token。它是纯函数，导出以便调用方复用同一套
// 切词规则（例如技能排序可以共享分词语义而不共享实现）。
//
// 规则：
//   - ASCII 单词按 camelCase / 字母数字边界切分并小写化；
//   - 英文复数做保守归一（ies→y、es→""、结尾 s→""）；
//   - CJK 连续段同时产生单字与相邻 bigram，使单字与短语查询都能命中；
//   - 停用词与空 token 被丢弃。
func Tokenize(s string) []string {
	var out []string
	var ascii strings.Builder
	var cjk []rune

	flushASCII := func() {
		if ascii.Len() == 0 {
			return
		}
		word := normalizeWord(ascii.String())
		ascii.Reset()
		if word != "" && !stopwords[word] {
			out = append(out, word)
		}
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		for _, r := range cjk {
			out = append(out, string(r))
		}
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i])+string(cjk[i+1]))
		}
		cjk = cjk[:0]
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case isASCIIWord(r):
			if len(cjk) > 0 {
				flushCJK()
			}
			// camelCase：小写→大写、字母↔数字，以及全大写缩写→首字母小写的新词
			// （HTTPServer → http / server）边界处切分。
			var next rune
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if ascii.Len() > 0 && shouldSplit(lastASCIIRune(ascii.String()), r, next) {
				flushASCII()
			}
			ascii.WriteRune(r)
		case isCJK(r):
			if ascii.Len() > 0 {
				flushASCII()
			}
			// 有界输入：单个 CJK 段超过 256 字时截断，避免超长声明性文本放大索引。
			if len(cjk) >= 256 {
				flushCJK()
			}
			cjk = append(cjk, r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

func isASCIIWord(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func isCJK(r rune) bool {
	return r >= 0x2e80 && r <= 0x9fff || r >= 0xf900 && r <= 0xfaff || r >= 0xff00 && r <= 0xffef
}

func lastASCIIRune(s string) rune {
	for i := len(s) - 1; i >= 0; i-- {
		return rune(s[i])
	}
	return 0
}

func shouldSplit(prev, cur, next rune) bool {
	return unicode.IsLower(prev) && unicode.IsUpper(cur) ||
		unicode.IsLetter(prev) && unicode.IsDigit(cur) ||
		unicode.IsDigit(prev) && unicode.IsLetter(cur) ||
		unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next)
}

// normalizeWord 小写化并做保守复数归一。
func normalizeWord(w string) string {
	w = strings.ToLower(w)
	if len(w) <= 2 {
		return w
	}
	switch {
	case len(w) > 3 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "es") && !strings.HasSuffix(w, "ses"):
		return w[:len(w)-2]
	case len(w) > 2 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

// Rank 对 docs 按与 query 的 BM25 相关性排序。返回的新切片按分数降序，
// 同分保持输入顺序（稳定排序）。只有命中 query 中全部不同 token 的文档
// 才进入结果——这与旧的手写工具搜索语义一致，避免多词查询只命中一小部分词
// 就返回一堆弱相关工具。
func Rank(query string, docs []Doc) []Scored {
	q := unique(Tokenize(query))
	if len(q) == 0 || len(docs) == 0 {
		return nil
	}

	fields := make([][3][]string, len(docs))
	union := make([]map[string]bool, len(docs))
	df := make(map[string]int, len(q))
	totalLen := 0
	for i, doc := range docs {
		fields[i] = [3][]string{
			Tokenize(doc.Name),
			Tokenize(doc.Desc),
			Tokenize(strings.Join(doc.Props, " ")),
		}
		seen := make(map[string]bool, len(fields[i][0])+len(fields[i][1])+len(fields[i][2]))
		docLen := 0
		for _, field := range fields[i] {
			for _, tok := range field {
				seen[tok] = true
				docLen++
			}
		}
		union[i] = seen
		totalLen += docLen
	}
	for _, tok := range q {
		for _, seen := range union {
			if seen[tok] {
				df[tok]++
			}
		}
	}
	avgdl := 1.0
	if len(docs) > 0 {
		avgdl = float64(totalLen) / float64(len(docs))
	}
	if avgdl <= 0 {
		avgdl = 1
	}

	n := float64(len(docs))
	out := make([]Scored, 0, len(docs))
	for i, doc := range docs {
		docLen := len(fields[i][0]) + len(fields[i][1]) + len(fields[i][2])
		covered := true
		for _, tok := range q {
			if !union[i][tok] {
				covered = false
				break
			}
		}
		if !covered {
			continue
		}
		score := 0.0
		for fi, field := range fields[i] {
			tf := make(map[string]int, len(field))
			for _, tok := range field {
				tf[tok]++
			}
			for _, tok := range q {
				f := tf[tok]
				if f == 0 {
					continue
				}
				idf := 1 + (n-float64(df[tok])+0.5)/(float64(df[tok])+0.5)
				denom := float64(f) + bm25K1*(1-bm25B+bm25B*float64(docLen)/avgdl)
				score += fieldWeights[fi] * idf * (float64(f) * (bm25K1 + 1)) / denom
			}
		}
		if score > 0 {
			out = append(out, Scored{Doc: doc, Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func unique(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// schemaCache 缓存 schema 文本 → 属性名列表。schema 字符串在进程内反复出现
// （每个 Specs() 调用都会重建），按内容缓存让 tool_search 的每次查询免于重复解析。
var schemaCache sync.Map

// IndexSchema 从 JSON schema 收集属性名，跟随本地 $ref（#/...），并缓存结果。
// 非法 JSON 与空输入返回 nil；递归深度与访问节点数都有上界。
func IndexSchema(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	key := string(raw)
	if cached, ok := schemaCache.Load(key); ok {
		return cached.([]string)
	}
	props := collectSchemaProps(raw)
	schemaCache.Store(key, props)
	return props
}

// clearSchemaCache 仅供测试使用，清空 schema 属性缓存。
func clearSchemaCache() { schemaCache = sync.Map{} }

func collectSchemaProps(raw json.RawMessage) []string {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	c := &schemaCollector{root: root, seen: make(map[string]bool)}
	c.visit(root, 0)
	// map 遍历顺序不稳定：排序+去重让缓存结果与测试 golden 都可重现。
	sort.Strings(c.props)
	out := c.props[:0]
	for _, prop := range c.props {
		if len(out) == 0 || out[len(out)-1] != prop {
			out = append(out, prop)
		}
	}
	return out
}

type schemaCollector struct {
	root  any
	props []string
	seen  map[string]bool
	nodes int
}

func (c *schemaCollector) visit(node any, depth int) {
	if node == nil || depth > maxSchemaDepth || c.nodes >= maxSchemaNodes {
		return
	}
	c.nodes++
	m, ok := node.(map[string]any)
	if !ok {
		if arr, ok := node.([]any); ok {
			for _, item := range arr {
				c.visit(item, depth+1)
			}
		}
		return
	}

	if ref, ok := m["$ref"].(string); ok && strings.HasPrefix(ref, "#/") && len(ref) <= maxSchemaRefLen {
		if target := resolvePointer(c.root, ref); target != nil && !c.seen[ref] {
			c.seen[ref] = true
			c.visit(target, depth+1)
		}
	}

	if properties, ok := m["properties"].(map[string]any); ok {
		for name, sub := range properties {
			if len(c.props) < maxSchemaNodes && name != "" {
				c.props = append(c.props, name)
			}
			c.visit(sub, depth+1)
		}
	}
	for _, key := range []string{"items", "definitions", "$defs", "additionalProperties", "anyOf", "oneOf", "allOf", "not"} {
		if sub, ok := m[key]; ok {
			c.visit(sub, depth+1)
		}
	}
}

// resolvePointer 解析以 "#/" 开头的本地 JSON Pointer。只支持对象键与数组下标，
// 键中的 ~1/~0 转义按 JSON Pointer 规范还原。
func resolvePointer(root any, ref string) any {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	cur := root
	for _, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			cur = node[part]
		case []any:
			idx := 0
			for _, r := range part {
				if r < '0' || r > '9' {
					return nil
				}
				idx = idx*10 + int(r-'0')
			}
			if idx >= len(node) {
				return nil
			}
			cur = node[idx]
		default:
			return nil
		}
		if cur == nil {
			return nil
		}
	}
	return cur
}
