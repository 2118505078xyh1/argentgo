package codegraph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxSnippetBytes 是单条边的源码截上限。截断按 rune 进行，
// 绝不把 CJK 字符切成半个 UTF-8 序列；末尾的 "..." 计入上限。
const maxSnippetBytes = 120

// fileCtx 保存单个文件的解析上下文。它刻意只依赖本文件的 AST，
// 使 parseGoFile 成为 (rel, src) 的纯函数，便于 mtime 缓存复用。
type fileCtx struct {
	pkg      string
	rel      string
	fset     *token.FileSet
	lines    []string
	imports  map[string]string // 源码别名 -> 包名字（取 import path 末段）
	recvVars map[string]string // 接收者变量名 -> 接收者基础类型名
	handled  map[ast.Node]bool // 已由更具体的 Rule 解释过的节点，避免重复出边
	symbols  []Symbol
	edges    []Edge
}

// scope 是 walker 当前的词法范围（函数或类型），用于填写 From/Enclosing。
type scope struct {
	name string
	sym  Symbol
}

// parseGoFile 解析一个文件的定义与引用边。语法错误被容忍：
// go/parser 会连同错误一起返回部分 AST，部分图谱总比没有图谱好。
func parseGoFile(rel string, src []byte) *fileData {
	fd := &fileData{lines: splitLines(src)}
	fset := token.NewFileSet()
	file, _ := parser.ParseFile(fset, rel, src, 0)
	if file == nil {
		return fd
	}
	ctx := &fileCtx{
		rel:      rel,
		fset:     fset,
		lines:    fd.lines,
		imports:  make(map[string]string),
		recvVars: make(map[string]string),
		handled:  make(map[ast.Node]bool),
	}
	if file.Name != nil {
		ctx.pkg = file.Name.Name
	}
	for _, spec := range file.Imports {
		importSpec(ctx, spec)
	}
	for _, decl := range file.Decls {
		collectReceiverVars(ctx, decl)
	}
	fd.symbols = extractSymbols(ctx, file)
	fd.edges = extractEdges(ctx, file)
	return fd
}

// splitLines 把源码切成行（兼容 LF/CRLF/CR），供 snippetAt 取证据行。
func splitLines(src []byte) []string {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

// snippetAt 取第 line 行的去空白源码，按 rune 截到 ≤120 字节。
func snippetAt(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	s := strings.TrimSpace(lines[line-1])
	if len(s) <= maxSnippetBytes {
		return s
	}
	limit := maxSnippetBytes - len("...")
	var b strings.Builder
	n := 0
	for _, r := range s {
		w := utf8.RuneLen(r)
		if w < 0 {
			w = 1
		}
		if n+w > limit {
			break
		}
		b.WriteRune(r)
		n += w
	}
	return b.String() + "..."
}

// importSpec 记录 import 别名到包名字的映射。别名 "_" 与 "." 不参与限定，
// 因此直接跳过；无别名时用 import path 的末段作为名字。
func importSpec(ctx *fileCtx, spec *ast.ImportSpec) {
	if spec == nil {
		return
	}
	p, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return
	}
	base := path.Base(p)
	alias := base
	if spec.Name != nil {
		if spec.Name.Name == "_" || spec.Name.Name == "." {
			return
		}
		alias = spec.Name.Name
	}
	ctx.imports[alias] = base
}

// receiverName 把 *T、T[P] 及带括号的接收者解包为基础类型标识符。
// 无法识别的表达式返回空串，此时方法仍会被列出（只是没有限定词）。
func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.ParenExpr:
		return receiverName(t.X)
	case *ast.IndexExpr: // 泛型接收者 T[P]
		return receiverName(t.X)
	case *ast.IndexListExpr: // 泛型接收者 T[P, Q]
		return receiverName(t.X)
	default:
		return ""
	}
}

// collectReceiverVars 建立"接收者变量名 -> 类型名"的局部映射。
// 这是纯语法推断：同名变量在多个方法中对应不同类型时，取源码最先出现者。
func collectReceiverVars(ctx *fileCtx, decl ast.Decl) {
	d, ok := decl.(*ast.FuncDecl)
	if !ok || d.Recv == nil || len(d.Recv.List) == 0 {
		return
	}
	typ := receiverName(d.Recv.List[0].Type)
	for _, nm := range d.Recv.List[0].Names {
		if nm == nil {
			continue
		}
		if _, exists := ctx.recvVars[nm.Name]; !exists {
			ctx.recvVars[nm.Name] = typ
		}
	}
}

// posOf 把 token.Pos 转成 (line, col)。
func (ctx *fileCtx) posOf(p token.Pos) (int, int) {
	position := ctx.fset.Position(p)
	return position.Line, position.Column
}

// extractSymbols 抽取顶层 FuncDecl / GenDecl 定义，与 repo_map 的符号视图同源，
// 但本包是生产包，因此只复制思想、不 import internal/tools。
func extractSymbols(ctx *fileCtx, file *ast.File) []Symbol {
	var out []Symbol
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name == nil {
				continue
			}
			line, col := ctx.posOf(d.Name.Pos())
			sym := Symbol{
				Name: d.Name.Name, Kind: "func", Pkg: ctx.pkg,
				File: ctx.rel, Line: line, Col: col,
			}
			if d.Recv != nil && len(d.Recv.List) > 0 {
				sym.Receiver = receiverName(d.Recv.List[0].Type)
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
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					if sp.Name == nil {
						continue
					}
					line, col := ctx.posOf(sp.Name.Pos())
					out = append(out, Symbol{
						Name: sp.Name.Name, Kind: kind, Pkg: ctx.pkg,
						File: ctx.rel, Line: line, Col: col,
					})
				case *ast.ValueSpec:
					for _, nm := range sp.Names {
						if nm == nil {
							continue
						}
						line, col := ctx.posOf(nm.Pos())
						out = append(out, Symbol{
							Name: nm.Name, Kind: kind, Pkg: ctx.pkg,
							File: ctx.rel, Line: line, Col: col,
						})
					}
				}
			}
		}
	}
	return out
}

// edgeVisitor 实现 ast.Visitor。作用域随 Visit 返回的"子 visitor"向下传递，
// 而不依赖 ast.Walk 的 Visit(nil) 回调——后者对 nil 子节点会被调用多次，
// 用栈配对很容易错配，导致 Enclosing 归属错误。
type edgeVisitor struct {
	ctx      *fileCtx
	sc       scope
	hasScope bool
}

// addEdge 生成一条尚未消解的边，并附上源码证据（Rule/At/Snippet）。
func (v *edgeVisitor) addEdge(n ast.Node, to Ref, rule string) {
	if to.Name == "" || n == nil {
		return
	}
	line, col := v.ctx.posOf(n.Pos())
	e := Edge{
		To: to, Rule: rule,
		At:      Pos{File: v.ctx.rel, Line: line, Col: col},
		Snippet: snippetAt(v.ctx.lines, line),
	}
	if v.hasScope {
		e.From = v.sc.sym
		e.Enclosing = v.sc.name
	}
	v.ctx.edges = append(v.ctx.edges, e)
}

// Visit 按节点类型出边；遇到 FuncDecl/TypeSpec 时返回一个带新作用域的子 visitor。
func (v *edgeVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}
	switch d := node.(type) {
	case *ast.FuncDecl:
		if d.Name == nil {
			return v
		}
		line, col := v.ctx.posOf(d.Name.Pos())
		sym := Symbol{
			Name: d.Name.Name, Kind: "func", Pkg: v.ctx.pkg,
			File: v.ctx.rel, Line: line, Col: col,
		}
		if d.Recv != nil && len(d.Recv.List) > 0 {
			sym.Receiver = receiverName(d.Recv.List[0].Type)
			if sym.Receiver != "" {
				sym.Kind = "method"
			}
		}
		return &edgeVisitor{ctx: v.ctx, hasScope: true, sc: scope{name: enclosingName(sym), sym: sym}}
	case *ast.TypeSpec:
		if d.Name == nil {
			return v
		}
		line, col := v.ctx.posOf(d.Name.Pos())
		sym := Symbol{
			Name: d.Name.Name, Kind: "type", Pkg: v.ctx.pkg,
			File: v.ctx.rel, Line: line, Col: col,
		}
		child := &edgeVisitor{ctx: v.ctx, hasScope: true, sc: scope{name: d.Name.Name, sym: sym}}
		if d.Type != nil {
			collectTypeRefs(v.ctx, child, d.Type)
		}
		return child
	case *ast.CallExpr:
		if ref, rule, ok := callRef(v.ctx, d.Fun); ok {
			markHandled(d.Fun, v.ctx.handled)
			v.addEdge(d.Fun, ref, rule)
		}
	case *ast.CompositeLit:
		if d.Type != nil {
			if ref, ok := typeRef(v.ctx, d.Type); ok {
				markHandled(d.Type, v.ctx.handled)
				v.addEdge(d.Type, ref, "composite")
			}
		}
	case *ast.SelectorExpr:
		if v.ctx.handled[d] {
			return v
		}
		if ref, _, ok := selectorRef(v.ctx, d); ok {
			// 非调用位置的选择器统一记为 field，保留 Pkg/Receiver 限定词。
			v.addEdge(d, ref, "field")
		}
	}
	return v
}

// extractEdges 遍历全 AST，按 call/method_call/type_ref/composite/field 五类 Rule 出边。
func extractEdges(ctx *fileCtx, file *ast.File) []Edge {
	ast.Walk(&edgeVisitor{ctx: ctx}, file)
	return ctx.edges
}

// enclosingName 给方法作用域加上接收者限定，便于人读与查询过滤。
func enclosingName(sym Symbol) string {
	if sym.Receiver != "" {
		return sym.Receiver + "." + sym.Name
	}
	return sym.Name
}

// collectTypeRefs 在 TypeSpec.Type 子树中收集类型引用（含 struct 字段类型），
// 并把解释过的节点标记为 handled，避免外层 walker 再 emit 一条 field 边。
func collectTypeRefs(ctx *fileCtx, v *edgeVisitor, expr ast.Expr) {
	ast.Inspect(expr, func(n ast.Node) bool {
		switch t := n.(type) {
		case *ast.Ident:
			if t.Name == "_" {
				return true
			}
			ctx.handled[t] = true
			v.addEdge(t, Ref{Name: t.Name, Pkg: ctx.pkg}, "type_ref")
		case *ast.SelectorExpr:
			if ref, ok := typeRef(ctx, t); ok {
				markHandled(t, ctx.handled)
				v.addEdge(t, ref, "type_ref")
				return false
			}
		}
		return true
	})
}

// typeRef 把类型位置的表达式转成 Ref：裸标识符带本包限定，
// pkg.T 用 import 别名解析到包名字，其他形态（如 map/slice）忽略。
func typeRef(ctx *fileCtx, expr ast.Expr) (Ref, bool) {
	switch t := expr.(type) {
	case *ast.Ident:
		if t.Name == "" || t.Name == "_" {
			return Ref{}, false
		}
		return Ref{Name: t.Name, Pkg: ctx.pkg}, true
	case *ast.SelectorExpr:
		if t.Sel == nil || t.Sel.Name == "" {
			return Ref{}, false
		}
		ref := Ref{Name: t.Sel.Name}
		if x, ok := t.X.(*ast.Ident); ok {
			if pkgName, found := ctx.imports[x.Name]; found {
				ref.Pkg = pkgName
			}
		}
		return ref, true
	case *ast.ParenExpr:
		return typeRef(ctx, t.X)
	default:
		return Ref{}, false
	}
}

// callRef 解析 CallExpr.Fun。普通标识符是 call；选择器是包函数（call）
// 或方法调用（method_call），后者在能识别接收者变量时带上 Receiver 限定。
func callRef(ctx *fileCtx, fun ast.Expr) (Ref, string, bool) {
	switch f := fun.(type) {
	case *ast.Ident:
		return Ref{Name: f.Name, Pkg: ctx.pkg}, "call", f.Name != ""
	case *ast.SelectorExpr:
		ref, rule, ok := selectorRef(ctx, f)
		return ref, rule, ok
	case *ast.IndexExpr: // f[T]()
		return callRef(ctx, f.X)
	case *ast.IndexListExpr: // f[T, U]()
		return callRef(ctx, f.X)
	case *ast.ParenExpr:
		return callRef(ctx, f.X)
	default:
		return Ref{}, "", false
	}
}

// selectorRef 把 x.Sel 转成 Ref 并给出 Rule：
// 已知 import 别名 -> 包函数/字段，已知接收者变量 -> method_call，
// 其余（局部变量、链式选择器）保持裸名，留给 resolver 判为 candidate。
func selectorRef(ctx *fileCtx, sel *ast.SelectorExpr) (Ref, string, bool) {
	if sel == nil || sel.Sel == nil || sel.Sel.Name == "" {
		return Ref{}, "", false
	}
	ref := Ref{Name: sel.Sel.Name}
	rule := "method_call"
	if x, ok := sel.X.(*ast.Ident); ok {
		if pkgName, found := ctx.imports[x.Name]; found {
			ref.Pkg = pkgName
			rule = "call"
			return ref, rule, true
		}
		if recv, found := ctx.recvVars[x.Name]; found {
			ref.Receiver = recv
			ref.Pkg = ctx.pkg
			return ref, rule, true
		}
	}
	return ref, rule, true
}

// markHandled 把表达式子树里的 Ident/SelectorExpr 标记为已解释，
// 防止同一处源码同时产出 call 与 field 两条边。
func markHandled(expr ast.Expr, handled map[ast.Node]bool) {
	ast.Inspect(expr, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.Ident, *ast.SelectorExpr:
			handled[n] = true
		}
		return true
	})
}
