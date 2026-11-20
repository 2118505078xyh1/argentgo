package tools

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// repoMapSymbolTokenBudget is the approximate token ceiling for the ranked
// symbol section. Tokens are estimated as len(bytes)/4, matching the rest of
// the tool's rough budgeting. The file-path view is emitted in full as before.
const repoMapSymbolTokenBudget = 2000

// repoSymbol is one top-level Go declaration found by the repo map.
// Receiver is set for methods and is used only for display; Name stays the
// bare identifier so mention matching works uniformly.
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

// repoReceiverName unwraps *T, T[P] and parenthesised receivers down to the
// base type identifier. Unknown expression shapes yield an empty string, in
// which case the method is still listed (just without a qualifier).
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

// extractRepoSymbols parses every Go file under base and returns its top-level
// declarations. Syntax errors are tolerated: go/parser returns the partial AST
// alongside the error, and a partial map is more useful than no map. Non-Go
// files are ignored for now (no lightweight lexer for other languages yet).
func extractRepoSymbols(base string, relFiles []string) []repoSymbol {
	files := append([]string(nil), relFiles...)
	sort.Strings(files)

	var out []repoSymbol
	for _, rel := range files {
		if !strings.HasSuffix(strings.ToLower(rel), ".go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		fset := token.NewFileSet()
		file, _ := parser.ParseFile(fset, rel, src, 0)
		if file == nil {
			continue
		}
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
	}
	return out
}

// repoParseMentions splits free-form mention text into identifier-like tokens
// used for symbol-name matching. File paths are matched separately (as whole
// lowercased strings) in rankRepoSymbols.
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

// repoBaseScore is the mention-independent prior. Entry points, exported
// declarations and "declaration-like" kinds rank higher; long names and deep
// line positions are mildly penalised. Both penalties are capped so they can
// never reorder two otherwise identical symbols by accident.
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

// rankRepoSymbols scores symbols then sorts with a total-order tiebreak, so
// the result is byte-for-byte deterministic regardless of file walk or map
// iteration order.
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

func repoMapTokenEstimate(s string) int {
	n := len(s) / 4
	if n < 1 {
		return 1
	}
	return n
}

// budgetRepoSymbols keeps the longest prefix of ranked symbols whose combined
// estimated tokens fit the budget. Prefix sums plus sort.Search give a binary
// search over the cut point (O(n log n)); no partial lines are ever emitted.
func budgetRepoSymbols(ranked []repoSymbol, budget int) []repoSymbol {
	if budget <= 0 || len(ranked) == 0 {
		return nil
	}
	prefix := make([]int, len(ranked)+1)
	for i, sym := range ranked {
		prefix[i+1] = prefix[i] + repoMapTokenEstimate(formatRepoSymbol(sym))
	}
	// First prefix whose cost exceeds the budget; the answer is the one before.
	n := sort.Search(len(prefix), func(i int) bool { return prefix[i] > budget }) - 1
	if n < 0 {
		n = 0
	}
	return ranked[:n]
}

// repoMapSymbolSection renders the ranked, budgeted symbol list as extra lines
// appended to the standard repo map output.
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
