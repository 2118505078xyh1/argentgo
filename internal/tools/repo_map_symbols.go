package tools

import (
	"fmt"
	"go/ast"
	"go/token"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"example.com/argentgo/internal/astcache"
	"example.com/argentgo/internal/tokenize"
)

// repoMapSymbolTokenBudget 是排名符号区的近似 token 上限。
// 成本来自 estimateRepoTokens，它单独计算宽 CJK
// rune，使中文密集的 repo 地图无法悄悄突破预算。
// 文件路径视图照旧完整输出。
const repoMapSymbolTokenBudget = 2000

// repoSymbol 是 repo 地图找到的一个顶层 Go 声明。
// 方法会设置 Receiver，它仅用于展示；Name 保持
// 裸标识符，使提及匹配统一进行。
type repoSymbol struct {
	File     string // slash-separated, relative to the mapped root
	Name     string
	Receiver string
	Line     int
	Kind     string // func, method, type, const, var
	Exported bool
}

type rankedRepoSymbol struct {
	sym   repoSymbol
	score float64
}

// repoReceiverName 把 *T、T[P] 和带括号的接收者解包为
// 基础类型标识符。未知的表达式形式返回空字符串，
// 此时方法仍会被列出（只是没有限定符）。
func repoReceiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return repoReceiverName(t.X)
	case *ast.ParenExpr:
		return repoReceiverName(t.X)
	case *ast.IndexExpr: // generic receiver T[P]
		return repoReceiverName(t.X)
	case *ast.IndexListExpr: // generic receiver T[P, Q]
		return repoReceiverName(t.X)
	default:
		return ""
	}
}

// extractRepoSymbols 解析 base 下每个 Go 文件，返回其顶层
// 声明。语法错误被容忍：go/parser 会连同错误一起返回部分 AST，
// 而部分地图总比没有地图有用。目前忽略非 Go
// 文件（尚无针对其他语言的轻量词法分析器）。
//
// 解析走进程内的符号缓存，使长寿命进程
// （RPC/serve）在重复的 repo-map 调用间复用先前的解析。缓存
// 包装器与原始的"读取并解析"逻辑逐字节一致。
func extractRepoSymbols(base string, relFiles []string) []repoSymbol {
	return cachedRepoSymbolsForFiles(base, relFiles)
}

// parseGoSource 把某个 Go 文件的源码解析为顶层声明。它
// 刻意是 (rel, src) 的纯函数，使 mtime 符号缓存能在
// 未命中时复用完全相同的解析逻辑。语法错误被
// 容忍：go/parser 会连同错误一起返回部分 AST。
func parseGoSource(rel string, src []byte) []repoSymbol {
	// 内容寻址缓存：repo_map 的 mtime 缓存按变更时间失效，astcache 按内容
	// 去重，两者互补（内容未变但 mtime 变化时仍能命中）。
	file, fset, _ := astcache.ParseDefault(rel, "go", src, 0)
	if file == nil {
		return nil
	}
	var out []repoSymbol
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			sym := repoSymbol{
				File:     rel,
				Name:     d.Name.Name,
				Line:     fset.Position(d.Pos()).Line,
				Kind:     "func",
				Exported: ast.IsExported(d.Name.Name),
			}
			if d.Recv != nil && len(d.Recv.List) > 0 {
				sym.Receiver = repoReceiverName(d.Recv.List[0].Type)
				if sym.Receiver != "" {
					sym.Kind = "method"
				}
			}
			out = append(out, sym)
		case *ast.GenDecl:
			kind := ""
			switch d.Tok {
			case token.TYPE:
				kind = "type"
			case token.CONST:
				kind = "const"
			case token.VAR:
				kind = "var"
			default:
				continue
			}
			for _, spec := range d.Specs {
				line := fset.Position(spec.Pos()).Line
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					if sp.Name == nil {
						continue
					}
					out = append(out, repoSymbol{
						File: rel, Name: sp.Name.Name, Line: line,
						Kind: kind, Exported: ast.IsExported(sp.Name.Name),
					})
				case *ast.ValueSpec:
					for _, name := range sp.Names {
						if name == nil {
							continue
						}
						out = append(out, repoSymbol{
							File: rel, Name: name.Name, Line: line,
							Kind: kind, Exported: ast.IsExported(name.Name),
						})
					}
				}
			}
		}
	}
	return out
}

// repoParseMentions 把自由格式的提及文本切成类似标识符的
// 标记，用于符号名匹配。文件路径另行匹配（作为整体
// 小写字符串）在 rankRepoSymbols 中使用。
func repoParseMentions(mentions []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, raw := range mentions {
		for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
		}) {
			low := strings.ToLower(tok)
			if utf8.RuneCountInString(low) < 2 || seen[low] {
				continue
			}
			seen[low] = true
			out = append(out, low)
		}
	}
	return out
}

// repoBaseScore 是与提及无关的基础分。入口点、导出的
// 声明以及"类似声明"的类别排名更高；较长的名字和较深的
// 行位置会被轻微扣分。两项扣分都设有上限，因此它们
// 绝不会意外地重排两个其他方面相同的符号。
func repoBaseScore(s repoSymbol) float64 {
	score := 0.0
	switch s.Kind {
	case "func":
		score += 8
	case "type":
		score += 6
	case "method":
		score += 5
	case "const", "var":
		score += 2
	}
	if s.Exported {
		score += 4
	}
	if repoMapEntryPoints[strings.ToLower(filepath.Base(s.File))] {
		score += 12
	}
	score -= math.Min(float64(utf8.RuneCountInString(s.Name))*0.15, 9)
	score -= math.Min(float64(s.Line)*0.01, 10)
	return score
}

// rankRepoSymbols 先给符号打分，再按全序的决胜规则排序，因此
// 结果逐字节确定，不受文件遍历顺序或 map
// 迭代顺序影响。
func rankRepoSymbols(syms []repoSymbol, mentions []string) []repoSymbol {
	tokens := repoParseMentions(mentions)
	paths := make([]string, 0, len(mentions))
	for _, raw := range mentions {
		p := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/")))
		if len(p) >= 2 {
			paths = append(paths, p)
		}
	}

	scored := make([]rankedRepoSymbol, 0, len(syms))
	for _, sym := range syms {
		score := repoBaseScore(sym)
		nameLow := strings.ToLower(sym.Name)
		for _, tok := range tokens {
			if nameLow == tok {
				score += 120 // exact identifier mention wins decisively
			} else if strings.Contains(nameLow, tok) || strings.Contains(tok, nameLow) {
				score += 24
			}
		}
		fileLow := strings.ToLower(sym.File)
		for _, p := range paths {
			if strings.Contains(fileLow, p) {
				score += 30 // whole path mention: e.g. "internal/agent"
			}
		}
		scored = append(scored, rankedRepoSymbol{sym: sym, score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		a, b := scored[i], scored[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.sym.File != b.sym.File {
			return a.sym.File < b.sym.File
		}
		if a.sym.Line != b.sym.Line {
			return a.sym.Line < b.sym.Line
		}
		if a.sym.Name != b.sym.Name {
			return a.sym.Name < b.sym.Name
		}
		return a.sym.Kind < b.sym.Kind
	})

	ranked := make([]repoSymbol, len(scored))
	for i, r := range scored {
		ranked[i] = r.sym
	}
	return ranked
}

func formatRepoSymbol(s repoSymbol) string {
	name := s.Name
	if s.Receiver != "" {
		name = s.Receiver + "." + name
	}
	return fmt.Sprintf("  * [%s] %s:%d %s", s.Kind, s.File, s.Line, name)
}

// estimateRepoTokens 估算 repo 地图某一行的 token 成本，使用
// 共享的 internal/tokenize 估算器，使 repo 地图、bash 的体量提示和
// agent 的预算循环都以相同方式计算 CJK。下限为一
// 保留了旧的空输入和微小输入下的 len(bytes)/4 最小值。
func estimateRepoTokens(s string) int {
	n := tokenize.Estimate(s)
	if n < 1 {
		return 1
	}
	return n
}

// budgetRepoSymbols 保留排名符号的最长前缀，其合计
// 估算 token 数符合预算。前缀和加上 sort.Search 给出对
// 切分点的二分查找（O(n log n)）；绝不输出部分行。
func budgetRepoSymbols(ranked []repoSymbol, budget int) []repoSymbol {
	if budget <= 0 || len(ranked) == 0 {
		return nil
	}
	prefix := make([]int, len(ranked)+1)
	for i, sym := range ranked {
		prefix[i+1] = prefix[i] + estimateRepoTokens(formatRepoSymbol(sym))
	}
	// 成本超过预算的第一个前缀；答案是它前面的那个。
	n := sort.Search(len(prefix), func(i int) bool { return prefix[i] > budget }) - 1
	if n < 0 {
		n = 0
	}
	return ranked[:n]
}

// repoMapSymbolSection 把排名、限额后的符号列表渲染为附加行，
// 追加到标准 repo 地图输出之后。
func repoMapSymbolSection(syms []repoSymbol, mentions []string, budget int) []string {
	ranked := rankRepoSymbols(syms, mentions)
	kept := budgetRepoSymbols(ranked, budget)

	lines := []string{fmt.Sprintf("symbols (%d):", len(kept))}
	if len(kept) == 0 {
		lines = append(lines, "  (none)")
		return lines
	}
	for _, sym := range kept {
		lines = append(lines, formatRepoSymbol(sym))
	}
	if omitted := len(ranked) - len(kept); omitted > 0 {
		lines = append(lines, fmt.Sprintf("... [%d symbols omitted by ~%d token budget]", omitted, budget))
	}
	return lines
}
