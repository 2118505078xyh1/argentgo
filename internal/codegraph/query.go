package codegraph

import "sort"

// Query 按名字与可选限定词检索图谱。四类 Direction 互斥语义：
//   - definitions：定义在何处（以 Rule=definition 的边表示）
//   - callers：谁引用/调用它（To 命中的入边）
//   - references：它引用/调用了谁（From 命中的出边）
//   - all：三者并集，边按源码位置去重
//
// Limit 作用于 Definitions+Edges+Candidates 的总条数（按此顺序分配额度），
// 发生丢弃时 Truncated=1；Limit<=0 表示不截断。
func (ix *Index) Query(q Query) Result {
	var res Result
	if ix == nil {
		return res
	}
	res.IndexedFiles = len(ix.files)
	res.TotalSymbols = len(ix.defs)

	dir := q.Direction
	if dir == "" {
		dir = "all"
	}
	matchRef := func(ref Ref) bool {
		return (q.Name == "" || ref.Name == q.Name) &&
			(q.Pkg == "" || ref.Pkg == q.Pkg) &&
			(q.Receiver == "" || ref.Receiver == q.Receiver)
	}
	matchFrom := func(sym Symbol) bool {
		return (q.Name == "" || sym.Name == q.Name) &&
			(q.Pkg == "" || sym.Pkg == q.Pkg) &&
			(q.Receiver == "" || sym.Receiver == q.Receiver)
	}

	defs := make([]Edge, 0)
	if dir == "definitions" || dir == "all" {
		for _, sym := range ix.defs {
			if !matchFrom(sym) {
				continue
			}
			var lines []string
			if fd := ix.files[sym.File]; fd != nil {
				lines = fd.lines
			}
			defs = append(defs, definitionEdge(sym, lines))
		}
	}

	// 收集命中边的下标后统一排序去重，保证 all 模式下自调用只出现一次。
	idxs := make([]int, 0)
	seen := make(map[int]bool)
	addIdx := func(i int) {
		if !seen[i] {
			seen[i] = true
			idxs = append(idxs, i)
		}
	}
	if dir == "callers" || dir == "all" {
		for i, e := range ix.edges {
			if matchRef(e.To) {
				addIdx(i)
			}
		}
	}
	if dir == "references" || dir == "all" {
		for i, e := range ix.edges {
			if matchFrom(e.From) {
				addIdx(i)
			}
		}
	}
	sort.Ints(idxs)

	exact := make([]Edge, 0)
	cands := make([]Edge, 0)
	for _, i := range idxs {
		e := ix.edges[i]
		if e.Conf == "exact" {
			exact = append(exact, e)
			continue
		}
		cands = append(cands, e)
		// 只有真正多命中才算歧义；零命中的是"无法证明"的候选。
		if len(ix.matchDefs(e.To)) > 1 {
			res.Ambiguous++
		}
	}

	defs, exact, cands, truncated := applyLimit(defs, exact, cands, q.Limit)
	res.Definitions = defs
	res.Edges = exact
	res.Candidates = cands
	if truncated {
		res.Truncated = 1
	}

	rules := make(map[string]bool)
	for _, list := range [][]Edge{res.Definitions, res.Edges, res.Candidates} {
		for _, e := range list {
			if e.Rule != "" {
				rules[e.Rule] = true
			}
		}
	}
	for rule := range rules {
		res.Rules = append(res.Rules, rule)
	}
	sort.Strings(res.Rules)
	return res
}

// definitionEdge 把一个定义包装成边，使 Result 的三个列表类型统一；
// 定义天然是 exact，并带定义行本身作为证据位置。
func definitionEdge(sym Symbol, lines []string) Edge {
	return Edge{
		From: sym,
		To:   Ref{Name: sym.Name, Pkg: sym.Pkg, Receiver: sym.Receiver},
		Rule: "definition", Conf: "exact",
		At:      Pos{File: sym.File, Line: sym.Line, Col: sym.Col},
		Snippet: snippetAt(lines, sym.Line),
	}
}

// applyLimit 按 Definitions -> Edges -> Candidates 的顺序分配额度。
// limit<=0 表示不截断。
func applyLimit(defs, exact, cands []Edge, limit int) ([]Edge, []Edge, []Edge, bool) {
	if limit <= 0 {
		return defs, exact, cands, false
	}
	left := limit
	truncated := false
	cut := func(xs []Edge) []Edge {
		if len(xs) <= left {
			left -= len(xs)
			return xs
		}
		truncated = true
		out := xs[:left]
		left = 0
		return out
	}
	defs = cut(defs)
	exact = cut(exact)
	cands = cut(cands)
	return defs, exact, cands, truncated
}
