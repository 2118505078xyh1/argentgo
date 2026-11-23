// Package astedit 提供 Go 源码的符号级结构搜索与改写原语。
//
// 实现只用标准库：go/parser 定位、go/ast 遍历、go/token 取字节偏移。
// 改写按字节区间 splice 完成，刻意不对整文件做 go/format，从而把 diff
// 限制在被改动的声明/调用附近，完整保留注释与原有排版（包括 CJK 注释）。
package astedit

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/astcache"
)

const (
	// MaxSourceBytes 是单个源文件与替换文本的最大字节数。
	// 超过该上限直接拒绝，避免超大输入耗尽内存。
	MaxSourceBytes = 2 << 20 // 2 MiB

	// maxDiffLinesForLCS 限制 Diff 构造 LCS 表的行数；
	// 超出时退化为"整段删除 + 整段新增"的视图，保持有界。
	maxDiffLinesForLCS = 4000
)

// 哨兵错误。调用方用 errors.Is 做分层匹配。
var (
	ErrTooLarge           = errors.New("astedit: input exceeds size limit")
	ErrInvalidSymbol      = errors.New("astedit: invalid symbol name")
	ErrSymbolNotFound     = errors.New("astedit: symbol not found")
	ErrUnparsable         = errors.New("astedit: source cannot be parsed")
	ErrInvalidReplacement = errors.New("astedit: replacement introduces invalid syntax")
)

// CallSite 描述一个函数调用点。Line/Column 均为 1 基，
// Args 是实参列表的原始文本（去掉两端空白）。
type CallSite struct {
	Line   int
	Column int
	Args   string
}

// span 是源码中的半开字节区间 [start, end)。
type span struct{ start, end int }

// parseSource 解析源码并返回文件 AST、文件集与语法错误数。
//
// parser.ParseFile 即便遇到错误也常返回可用的部分 AST；这里刻意容忍这类
// "部分源码"，只把错误数量带出去，供改写前后比较是否引入了新错误。
func parseSource(src []byte) (*ast.File, *token.FileSet, int) {
	// 内容寻址缓存：ast_edit / outline / 语法检查反复解析同一批文件，
	// parse 本身是热点（1MB 约 187ms），缓存命中约 0.2µs。
	f, fset, err := astcache.ParseDefault("", "go", src, parser.ParseComments)
	if err == nil {
		return f, fset, 0
	}
	if list, ok := err.(scanner.ErrorList); ok {
		return f, fset, len(list)
	}
	return f, fset, 1
}

// RewriteSymbol 在 src 中定位 symbol 并用 repl 的字节替换，返回新内容。
//
// 定位顺序：先找顶层声明（func/type/var/const），再找对同名函数的调用点。
//   - 顶层声明：单个 spec 的声明整段替换（repl 应包含 func/type/var/const 关键字）；
//     多 spec 的 GenDecl 只替换匹配的那一个 spec（repl 为该 spec 的文本）。
//   - 调用点：若没有同名声明，则替换所有对 symbol 的调用（符号级改写）。
//
// 替换后重新解析新内容；仅当语法错误数比原文件增加时才判为非法替换并报错，
// 这样既拒绝破坏语法的 repl，又不会把原本就有的"部分源码"误判为失败。
// 找不到声明或调用返回 ErrSymbolNotFound。
func RewriteSymbol(src []byte, symbol string, repl []byte) ([]byte, error) {
	if len(src) > MaxSourceBytes {
		return nil, fmt.Errorf("%w: source %d bytes (limit %d)", ErrTooLarge, len(src), MaxSourceBytes)
	}
	if len(repl) > MaxSourceBytes {
		return nil, fmt.Errorf("%w: replacement %d bytes (limit %d)", ErrTooLarge, len(repl), MaxSourceBytes)
	}
	if !token.IsIdentifier(symbol) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSymbol, symbol)
	}

	file, fset, baseErrs := parseSource(src)
	if file == nil {
		return nil, fmt.Errorf("%w: empty or unrecognizable source", ErrUnparsable)
	}

	var spans []span
	if s, ok := findDeclaration(file, fset, symbol); ok {
		spans = []span{s}
	} else {
		spans = findCallSpans(file, fset, symbol)
	}
	if len(spans) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrSymbolNotFound, symbol)
	}

	out := splice(src, spans, repl)
	_, _, newErrs := parseSource(out)
	if newErrs > baseErrs {
		return nil, fmt.Errorf("%w: rewritten source fails to parse", ErrInvalidReplacement)
	}
	return out, nil
}

// findDeclaration 返回名为 symbol 的顶层声明的字节区间。
func findDeclaration(file *ast.File, fset *token.FileSet, symbol string) (span, bool) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name != nil && d.Name.Name == symbol {
				return spanOffset(fset, d.Pos(), d.End()), true
			}
		case *ast.GenDecl:
			var matched span
			found := false
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					if sp.Name != nil && sp.Name.Name == symbol {
						matched, found = spanOffset(fset, sp.Pos(), sp.End()), true
					}
				case *ast.ValueSpec: // var / const
					for _, name := range sp.Names {
						if name.Name == symbol {
							matched, found = spanOffset(fset, sp.Pos(), sp.End()), true
						}
					}
				}
			}
			if !found {
				continue
			}
			if len(d.Specs) == 1 {
				// 单 spec：整段替换，repl 需自带 type/var/const 关键字。
				return spanOffset(fset, d.Pos(), d.End()), true
			}
			// 多 spec：只替换命中的 spec，避免误删同声明内的兄弟项。
			return matched, true
		}
	}
	return span{}, false
}

// findCallSpans 收集所有对 callee 的调用区间。
func findCallSpans(file *ast.File, fset *token.FileSet, callee string) []span {
	var spans []span
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || callName(call.Fun) != callee {
			return true
		}
		spans = append(spans, spanOffset(fset, call.Pos(), call.End()))
		return true
	})
	return spans
}

// callName 取调用表达式的被调函数名，支持 pkg.Func() 与 obj.Method() 形式。
func callName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// spanOffset 把 token.Pos 区间换算为相对 src 起点的字节区间。
func spanOffset(fset *token.FileSet, from, to token.Pos) span {
	return span{start: fset.Position(from).Offset, end: fset.Position(to).Offset}
}

// splice 按区间把 repl 写入 src；区间按起点排序并跳过重叠项，防止越界写。
func splice(src []byte, spans []span, repl []byte) []byte {
	ordered := append([]span(nil), spans...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })

	var out bytes.Buffer
	out.Grow(len(src) + len(repl)*len(ordered))
	prev := 0
	for _, s := range ordered {
		if s.start < prev || s.end < s.start || s.end > len(src) {
			continue
		}
		out.Write(src[prev:s.start])
		out.Write(repl)
		prev = s.end
	}
	out.Write(src[prev:])
	return out.Bytes()
}

// FindCalls 返回 src 中所有对 callee 的调用点，按源码位置排序。
func FindCalls(src []byte, callee string) ([]CallSite, error) {
	if len(src) > MaxSourceBytes {
		return nil, fmt.Errorf("%w: source %d bytes (limit %d)", ErrTooLarge, len(src), MaxSourceBytes)
	}
	if !token.IsIdentifier(callee) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidSymbol, callee)
	}

	file, fset, _ := parseSource(src)
	if file == nil {
		return nil, fmt.Errorf("%w: empty or unrecognizable source", ErrUnparsable)
	}

	var sites []CallSite
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || callName(call.Fun) != callee {
			return true
		}
		pos := fset.Position(call.Pos())
		// 直接切出左右括号之间的原始字节，保留实参里的 CJK 与排版。
		start := fset.Position(call.Lparen).Offset + 1
		end := fset.Position(call.Rparen).Offset
		if start > end {
			start = end
		}
		sites = append(sites, CallSite{
			Line:   pos.Line,
			Column: pos.Column,
			Args:   strings.TrimSpace(string(src[start:end])),
		})
		return true
	})
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}
		return sites[i].Column < sites[j].Column
	})
	return sites, nil
}

// Diff 生成 oldSrc 到 newSrc 的简单行 diff：
// 公共行为 " " 上下文，删除行为 "-"，新增行为 "+"。
// 输入被 CRLF 归一化后按行比较，因此仅换行符变化不会夸大结果。
func Diff(oldSrc, newSrc []byte) string {
	if bytes.Equal(oldSrc, newSrc) {
		return ""
	}
	a, b := splitLines(oldSrc), splitLines(newSrc)
	if len(a) > maxDiffLinesForLCS || len(b) > maxDiffLinesForLCS ||
		len(a)*len(b) > maxDiffLinesForLCS*maxDiffLinesForLCS {
		return renderWhole(a, b)
	}

	// 先收缩公共前后缀，既缩小 LCS 规模，也让输出更贴近实际改动。
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	midA, midB := a[pre:len(a)-suf], b[pre:len(b)-suf]

	n, m := len(midA), len(midB)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if midA[i-1] == midB[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	type op struct {
		sign byte
		text string
	}
	var ops []op
	i, j := n, m
	for i > 0 && j > 0 {
		switch {
		case midA[i-1] == midB[j-1]:
			ops = append(ops, op{' ', midA[i-1]})
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]:
			ops = append(ops, op{'-', midA[i-1]})
			i--
		default:
			ops = append(ops, op{'+', midB[j-1]})
			j--
		}
	}
	for i > 0 {
		ops = append(ops, op{'-', midA[i-1]})
		i--
	}
	for j > 0 {
		ops = append(ops, op{'+', midB[j-1]})
		j--
	}

	out := make([]string, 0, len(a)+len(b))
	for _, ln := range a[:pre] {
		out = append(out, " "+ln)
	}
	for k := len(ops) - 1; k >= 0; k-- {
		out = append(out, string(ops[k].sign)+ops[k].text)
	}
	for _, ln := range a[len(a)-suf:] {
		out = append(out, " "+ln)
	}
	return joinDiff(out)
}

// renderWhole 在输入过大时退化为整段替换视图。
func renderWhole(a, b []string) string {
	out := make([]string, 0, len(a)+len(b))
	for _, ln := range a {
		out = append(out, "-"+ln)
	}
	for _, ln := range b {
		out = append(out, "+"+ln)
	}
	return joinDiff(out)
}

// joinDiff 把带前缀的行拼成以换行结尾的文本；空输入返回空串。
func joinDiff(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// splitLines 归一化 CRLF 并按行切分，去掉结尾换行产生的空哨兵行。
func splitLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
