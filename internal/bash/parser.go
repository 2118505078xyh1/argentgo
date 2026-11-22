package bash

import (
	"fmt"
	"strings"
)

// ParseError 是语法错误。
type ParseError struct {
	Pos Pos
	Msg string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("shell: line %d col %d: %s", e.Pos.Line, e.Pos.Col, e.Msg)
}

// parser 用递归下降把 token 流建成 AST。极简文法：
//
//	list     := and_or ((';'|'\n') and_or)*
//	and_or   := pipeline (('&&'|'||') pipeline)*
//	pipeline := command ('|' command)*
//	command  := word (word|redirect)* | redirect+
//	redirect := ('>'|'>>'|'<'|'2>&1') word
//
// 遇到不支持的构造（子 shell、算术、后台）时产出 UnsupportedCmd，执行期报错。
// parser 只建结构，不做任何变量替换/去引号/glob。
type parser struct {
	tokens []Token
	i      int
	// stops 是控制结构解析时临时设置的"子语句终止保留字"集合。
	// 它只在期望命令词的位置生效，因此 echo then 里的 then 仍是普通参数。
	stops          map[string]bool
	stopDoubleSemi bool // case 分支体以 ';;' 结束
}

// setStops 临时替换终止词集合，返回恢复闭包（必须 defer 调用）。
func (p *parser) setStops(stops map[string]bool, doubleSemi bool) func() {
	oldStops, oldDouble := p.stops, p.stopDoubleSemi
	p.stops, p.stopDoubleSemi = stops, doubleSemi
	return func() { p.stops, p.stopDoubleSemi = oldStops, oldDouble }
}

func (p *parser) peekKind(n int) TokenKind {
	if p.i+n < len(p.tokens) {
		return p.tokens[p.i+n].Kind
	}
	return TokEOF
}

func (p *parser) isWord(s string) bool {
	return p.cur().Kind == TokWord && p.cur().Raw == s
}

func (p *parser) expectWord(s string) error {
	if !p.isWord(s) {
		return &ParseError{Pos: p.cur().Pos, Msg: "期望 " + s + "，得到 " + p.cur().Kind.String()}
	}
	p.next()
	return nil
}

// atStop 报告当前 token 是否终止了当前子语句。
func (p *parser) atStop() bool {
	cur := p.cur()
	if cur.Kind == TokEOF || cur.Kind == TokRBrace {
		return true
	}
	if p.stopDoubleSemi && cur.Kind == TokSemi {
		return true
	}
	return p.stops != nil && cur.Kind == TokWord && p.stops[cur.Raw]
}

func parse(tokens []Token) (*File, error) {
	p := &parser{tokens: tokens}
	stmt, err := p.parseList()
	if err != nil {
		return nil, err
	}
	if p.cur().Kind != TokEOF {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "意外的 token " + p.cur().Kind.String()}
	}
	return &File{Stmt: stmt}, nil
}

func (p *parser) cur() Token { return p.tokens[p.i] }
func (p *parser) next() Token {
	t := p.tokens[p.i]
	if p.i < len(p.tokens)-1 {
		p.i++
	}
	return t
}

// parseList 解析分号分隔的语句序列（左结合，用 BinaryCmd 的 && 语义近似顺序执行）。
// 注意：';' 的顺序语义与 '&&' 不同（前者无论成败都继续），这里用 BinaryOp 的
// Sequence 之外的处理——为保持 AST 精简，';' 仅在 File 顶层用 ListStmt 表达。
// 但本子集把多条分号语句也建成 OpSeq 的 BinaryCmd。见 OpSeq。
func (p *parser) parseList() (*Stmt, error) {
	// 空语句（如 fi 前没有命令）：返回一个空 Block，执行期视作成功空操作。
	// 注意先查 atStop 再跳分号：case 分支体的 ';;' 必须留给调用方识别。
	if p.atStop() {
		return &Stmt{Cmd: &Block{Pos: p.cur().Pos}, Pos: p.cur().Pos}, nil
	}
	// 跳过前导空语句（then/do/{ 之后的换行或分号）。
	for p.cur().Kind == TokSemi {
		p.next()
		if p.atStop() {
			return &Stmt{Cmd: &Block{Pos: p.cur().Pos}, Pos: p.cur().Pos}, nil
		}
	}
	left, err := p.parseAndOr()
	if err != nil {
		return nil, err
	}
	for p.cur().Kind == TokSemi && !p.atStop() {
		p.next()
		// 跳过空语句（连续分号/空行），但 case 分支体的 ';;' 要留给 atStop 识别。
		if !p.stopDoubleSemi {
			for p.cur().Kind == TokSemi {
				p.next()
			}
		}
		if p.atStop() {
			break
		}
		right, err := p.parseAndOr()
		if err != nil {
			return nil, err
		}
		left = &Stmt{
			Cmd: &BinaryCmd{Op: OpSeq, X: left, Y: right, Pos: left.Pos},
			Pos: left.Pos,
		}
	}
	return left, nil
}

func (p *parser) parseAndOr() (*Stmt, error) {
	left, err := p.parsePipeline()
	if err != nil {
		return nil, err
	}
	for p.cur().Kind == TokAnd || p.cur().Kind == TokOr {
		op := OpAnd
		if p.cur().Kind == TokOr {
			op = OpOr
		}
		p.next()
		right, err := p.parsePipeline()
		if err != nil {
			return nil, err
		}
		left = &Stmt{
			Cmd: &BinaryCmd{Op: op, X: left, Y: right, Pos: left.Pos},
			Pos: left.Pos,
		}
	}
	return left, nil
}

func (p *parser) parsePipeline() (*Stmt, error) {
	left, err := p.parseCommand()
	if err != nil {
		return nil, err
	}
	for p.cur().Kind == TokPipe {
		p.next()
		right, err := p.parseCommand()
		if err != nil {
			return nil, err
		}
		left = &Stmt{
			Cmd: &BinaryCmd{Op: OpPipe, X: left, Y: right, Pos: left.Pos},
			Pos: left.Pos,
		}
	}
	return left, nil
}

func (p *parser) parseCommand() (*Stmt, error) {
	tok := p.cur()
	pos := tok.Pos
	// 子语句的终止关键字处返回空块（execStmt 视作成功空操作）。
	if p.atStop() {
		return &Stmt{Cmd: &Block{Pos: pos}, Pos: pos}, nil
	}

	// 保留字只在命令词位置触发控制结构解析；作为参数出现时仍是普通单词。
	if tok.Kind == TokWord {
		switch tok.Raw {
		case "if":
			return p.parseIf()
		case "for":
			return p.parseFor()
		case "while", "until":
			return p.parseWhile(tok.Raw == "until")
		case "case":
			return p.parseCase()
		case "function":
			return p.parseFunctionDecl()
		case "select":
			// select 尚未实现：吞掉剩余 token，整条明确 fail-closed。
			p.i = len(p.tokens) - 1
			return &Stmt{Cmd: &UnsupportedCmd{Kind: "select", Pos: pos}, Pos: pos}, nil
		case "then", "else", "elif", "fi", "do", "done", "esac", "in":
			return nil, &ParseError{Pos: pos, Msg: "意外的关键字 " + tok.Raw}
		}
		// name() { ... } 形式的函数定义。
		if p.peekKind(1) == TokFuncParen {
			return p.parseFuncDeclAfterName()
		}
	}
	if tok.Kind == TokLBrace {
		return p.parseBlock()
	}

	var words []*Word
	var assigns []*Word
	var redirs []*Redirect

	for {
		t := p.cur()
		switch t.Kind {
		case TokEOF, TokSemi, TokAnd, TokOr, TokPipe, TokRParen, TokLBrace, TokRBrace, TokFuncParen:
			goto done
		case TokUnsupported:
			p.next()
			return &Stmt{Cmd: &UnsupportedCmd{Kind: t.Raw, Pos: pos}, Pos: pos}, nil
		case TokRedirOut, TokRedirAppend, TokRedirIn, TokRedirDupOut, TokRedirAll:
			r, err := p.parseRedirect()
			if err != nil {
				return nil, err
			}
			redirs = append(redirs, r)
		case TokWord:
			w, err := parseWord(t)
			if err != nil {
				return nil, &ParseError{Pos: t.Pos, Msg: err.Error()}
			}
			// 命令词之前出现的 NAME=VALUE 是赋值（独立语句或命令的临时环境）。
			// 一旦遇到非赋值词，其后的 NAME=VALUE 就是普通参数（如 echo A=1）。
			if len(words) == 0 && isAssignmentWord(t.Raw) {
				assigns = append(assigns, w)
			} else {
				words = append(words, w)
			}
			p.next()
		default:
			return nil, &ParseError{Pos: t.Pos, Msg: "意外的 token " + t.Kind.String()}
		}
	}
done:
	if len(words) == 0 && len(assigns) == 0 && len(redirs) == 0 {
		return nil, &ParseError{Pos: pos, Msg: "缺少命令"}
	}
	cmd := &CallExpr{Args: words, Assigns: assigns, Pos: pos}
	return &Stmt{Cmd: cmd, Redirs: redirs, Pos: pos}, nil
}

// isAssignmentWord 报告原始单词是否为 NAME=VALUE 形式的前导赋值。
// NAME 必须是合法的 shell 标识符（字母或下划线开头，后跟字母/数字/下划线）。
func isAssignmentWord(raw string) bool {
	eq := strings.IndexByte(raw, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := raw[i]
		valid := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' ||
			(i > 0 && c >= '0' && c <= '9')
		if !valid {
			return false
		}
	}
	return true
}

// ---- 控制结构解析 ----

// parseIf 解析 if cond; then body; elif cond; then body; else body; fi。
func (p *parser) parseIf() (*Stmt, error) {
	pos := p.next().Pos // if
	restore := p.setStops(map[string]bool{"then": true}, false)
	cond, err := p.parseList()
	restore()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("then"); err != nil {
		return nil, err
	}
	bodyStops := map[string]bool{"elif": true, "else": true, "fi": true}
	restore = p.setStops(bodyStops, false)
	thenBody, err := p.parseList()
	restore()
	if err != nil {
		return nil, err
	}
	var elifs []*ElifClause
	for p.isWord("elif") {
		p.next()
		restore = p.setStops(map[string]bool{"then": true}, false)
		ec, err := p.parseList()
		restore()
		if err != nil {
			return nil, err
		}
		if err := p.expectWord("then"); err != nil {
			return nil, err
		}
		restore = p.setStops(bodyStops, false)
		eb, err := p.parseList()
		restore()
		if err != nil {
			return nil, err
		}
		elifs = append(elifs, &ElifClause{Cond: ec, Body: eb})
	}
	var elseBody *Stmt
	if p.isWord("else") {
		p.next()
		restore = p.setStops(map[string]bool{"fi": true}, false)
		elseBody, err = p.parseList()
		restore()
		if err != nil {
			return nil, err
		}
	}
	if err := p.expectWord("fi"); err != nil {
		return nil, err
	}
	return &Stmt{Cmd: &IfCmd{Cond: cond, Then: thenBody, Elifs: elifs, Else: elseBody, Pos: pos}, Pos: pos}, nil
}

// parseFor 解析 for var in words; do body; done（无 in 时遍历位置参数）。
func (p *parser) parseFor() (*Stmt, error) {
	pos := p.next().Pos // for
	if p.cur().Kind != TokWord {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "for 期望变量名"}
	}
	varName := p.next().Raw
	var items []*Word
	if p.isWord("in") {
		p.next()
		for p.cur().Kind == TokWord && !p.isWord("do") {
			w, err := parseWord(p.cur())
			if err != nil {
				return nil, err
			}
			items = append(items, w)
			p.next()
		}
	}
	for p.cur().Kind == TokSemi {
		p.next()
	}
	if err := p.expectWord("do"); err != nil {
		return nil, err
	}
	restore := p.setStops(map[string]bool{"done": true}, false)
	body, err := p.parseList()
	restore()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("done"); err != nil {
		return nil, err
	}
	return &Stmt{Cmd: &ForCmd{Var: varName, Items: items, Body: body, Pos: pos}, Pos: pos}, nil
}

// parseWhile 解析 while/until cond; do body; done。
func (p *parser) parseWhile(until bool) (*Stmt, error) {
	pos := p.next().Pos // while / until
	restore := p.setStops(map[string]bool{"do": true}, false)
	cond, err := p.parseList()
	restore()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("do"); err != nil {
		return nil, err
	}
	restore = p.setStops(map[string]bool{"done": true}, false)
	body, err := p.parseList()
	restore()
	if err != nil {
		return nil, err
	}
	if err := p.expectWord("done"); err != nil {
		return nil, err
	}
	return &Stmt{Cmd: &WhileCmd{Cond: cond, Body: body, Until: until, Pos: pos}, Pos: pos}, nil
}

// parseCase 解析 case word in pat|pat) body;; ... esac。
func (p *parser) parseCase() (*Stmt, error) {
	pos := p.next().Pos // case
	if p.cur().Kind != TokWord {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "case 期望判别词"}
	}
	w, err := parseWord(p.cur())
	if err != nil {
		return nil, err
	}
	p.next()
	if err := p.expectWord("in"); err != nil {
		return nil, err
	}
	var clauses []*CaseClause
	for {
		for p.cur().Kind == TokSemi {
			p.next()
		}
		if p.isWord("esac") {
			break
		}
		if p.cur().Kind == TokEOF {
			return nil, &ParseError{Pos: p.cur().Pos, Msg: "case 缺少 esac"}
		}
		var pats []*Word
	collect:
		for {
			switch p.cur().Kind {
			case TokWord:
				pw, e := parseWord(p.cur())
				if e != nil {
					return nil, e
				}
				pats = append(pats, pw)
				p.next()
			case TokPipe:
				p.next() // 多模式分隔符 '|'
			case TokRParen:
				p.next()
				break collect
			default:
				return nil, &ParseError{Pos: p.cur().Pos, Msg: "case 模式期望 )"}
			}
		}
		restore := p.setStops(map[string]bool{"esac": true}, true)
		body, err := p.parseList()
		restore()
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, &CaseClause{Patterns: pats, Body: body})
	}
	p.next() // esac
	return &Stmt{Cmd: &CaseCmd{Word: w, Clauses: clauses, Pos: pos}, Pos: pos}, nil
}

// parseFunctionDecl 解析 function name [()] { body; }。
func (p *parser) parseFunctionDecl() (*Stmt, error) {
	pos := p.next().Pos // function
	if p.cur().Kind != TokWord {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "function 期望函数名"}
	}
	name := p.next().Raw
	if p.cur().Kind == TokFuncParen {
		p.next()
	}
	body, err := p.parseFuncBody(pos)
	if err != nil {
		return nil, err
	}
	return &Stmt{Cmd: &FuncDecl{Name: name, Body: body, Pos: pos}, Pos: pos}, nil
}

// parseFuncDeclAfterName 解析 name() { body; }。
func (p *parser) parseFuncDeclAfterName() (*Stmt, error) {
	tok := p.next() // 函数名
	p.next()        // ()
	body, err := p.parseFuncBody(tok.Pos)
	if err != nil {
		return nil, err
	}
	return &Stmt{Cmd: &FuncDecl{Name: tok.Raw, Body: body, Pos: tok.Pos}, Pos: tok.Pos}, nil
}

// parseFuncBody 解析 { body; }（花括号由 atStop 的 TokRBrace 终止）。
func (p *parser) parseFuncBody(pos Pos) (*Stmt, error) {
	if p.cur().Kind != TokLBrace {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "函数体期望 {"}
	}
	p.next()
	restore := p.setStops(nil, false)
	body, err := p.parseList()
	restore()
	if err != nil {
		return nil, err
	}
	if p.cur().Kind != TokRBrace {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "函数体期望 }"}
	}
	p.next()
	return body, nil
}

// parseBlock 解析 { body; } 复合命令。
func (p *parser) parseBlock() (*Stmt, error) {
	pos := p.next().Pos // {
	restore := p.setStops(nil, false)
	body, err := p.parseList()
	restore()
	if err != nil {
		return nil, err
	}
	if p.cur().Kind != TokRBrace {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "复合命令期望 }"}
	}
	p.next()
	return &Stmt{Cmd: &Block{Body: body, Pos: pos}, Pos: pos}, nil
}

func (p *parser) parseRedirect() (*Redirect, error) {
	t := p.next() // 操作符
	r := &Redirect{Pos: t.Pos}
	// 显式 fd 编码在 Raw 前缀里（如 "2>" → 2）；未指定时用默认值。
	fdOf := func(def int) int {
		if len(t.Raw) > 0 && t.Raw[0] >= '0' && t.Raw[0] <= '9' {
			return int(t.Raw[0] - '0')
		}
		return def
	}
	switch t.Kind {
	case TokRedirOut:
		r.Op, r.N = RedirOut, fdOf(1)
	case TokRedirAppend:
		r.Op, r.N = RedirAppend, fdOf(1)
	case TokRedirIn:
		r.Op, r.N = RedirIn, fdOf(0)
	case TokRedirDupOut:
		r.Op, r.N = RedirDupOut, fdOf(2) // 2>&1
		// 2>&1 没有目标单词。
		return r, nil
	case TokRedirAll:
		r.Op, r.N = RedirAll, 1
	}
	if p.cur().Kind != TokWord {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: "重定向缺少目标"}
	}
	w, err := parseWord(p.cur())
	if err != nil {
		return nil, &ParseError{Pos: p.cur().Pos, Msg: err.Error()}
	}
	r.Target = w
	p.next()
	return r, nil
}

// parseWord 把一个单词的原始文本组装成 Word（含引号/参数/命令替换 part）。
// 它只建结构，不展开任何值。
func parseWord(tok Token) (*Word, error) {
	src := []rune(tok.Raw)
	w := &Word{Pos: tok.Pos}
	var lit strings.Builder
	flushLit := func() {
		if lit.Len() > 0 {
			w.Parts = append(w.Parts, &LitPart{Text: lit.String()})
			lit.Reset()
		}
	}

	for i := 0; i < len(src); {
		r := src[i]
		switch {
		case r == '\\':
			// 反斜杠转义下一个字符，作为字面量。
			if i+1 < len(src) {
				lit.WriteRune(src[i+1])
				i += 2
			} else {
				i++
			}
		case r == '\'':
			// 单引号：收集到下一个单引号，整体作为 SglQuotedPart。
			i++
			var s strings.Builder
			for i < len(src) && src[i] != '\'' {
				s.WriteRune(src[i])
				i++
			}
			if i < len(src) {
				i++ // 闭合引号
			}
			flushLit()
			w.Parts = append(w.Parts, &SglQuotedPart{Text: s.String()})
		case r == '"':
			// 双引号：解析内部 part（展开但不分词）。
			i++
			inner, consumed, err := parseQuotedParts(src, i, '"')
			if err != nil {
				return nil, err
			}
			i = consumed
			flushLit()
			w.Parts = append(w.Parts, &DblQuotedPart{Parts: inner})
		case r == '$':
			part, consumed, err := parseDollar(src, i)
			if err != nil {
				return nil, err
			}
			flushLit()
			if part != nil {
				w.Parts = append(w.Parts, part)
			}
			i = consumed
		default:
			lit.WriteRune(r)
			i++
		}
	}
	flushLit()
	return w, nil
}

// parseQuotedParts 解析引号内（终止符为 quote）的 part 序列，返回结束后的下标。
func parseQuotedParts(src []rune, start int, quote rune) ([]WordPart, int, error) {
	var parts []WordPart
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, &LitPart{Text: lit.String()})
			lit.Reset()
		}
	}
	i := start
	for i < len(src) {
		r := src[i]
		if r == quote {
			flush()
			return parts, i + 1, nil
		}
		switch r {
		case '\\':
			if i+1 < len(src) {
				lit.WriteRune(src[i+1])
				i += 2
			} else {
				i++
			}
		case '$':
			part, consumed, err := parseDollar(src, i)
			if err != nil {
				return nil, 0, err
			}
			flush()
			if part != nil {
				parts = append(parts, part)
			}
			i = consumed
		default:
			lit.WriteRune(r)
			i++
		}
	}
	flush()
	return parts, i, nil // 未闭合（理论上 lexer 已拦截）
}

// parseDollar 解析 $VAR / ${VAR} / ${VAR:-def} / $? / $(cmd)。
// 返回 nil part 与推进后的下标（用于无法识别时按字面 '$' 处理）。
func parseDollar(src []rune, i int) (WordPart, int, error) {
	// '$' 后无内容。
	if i+1 >= len(src) {
		return &LitPart{Text: "$"}, i + 1, nil
	}
	next := src[i+1]
	switch {
	case next == '?':
		return &ParamPart{Kind: ParamLastStatus}, i + 2, nil
	case next >= '0' && next <= '9':
		if next == '0' {
			return &ParamPart{Kind: ParamScriptName}, i + 2, nil
		}
		return &ParamPart{Kind: ParamPos, Index: int(next - '0')}, i + 2, nil
	case next == '@':
		return &ParamPart{Kind: ParamArgsAll}, i + 2, nil
	case next == '*':
		return &ParamPart{Kind: ParamArgsStar}, i + 2, nil
	case next == '#':
		return &ParamPart{Kind: ParamArgCount}, i + 2, nil
	case next == '(':
		// 命令替换：找到配对的 ')'，内部命令递归解析。
		if i+2 < len(src) && src[i+2] == '(' {
			return nil, 0, fmt.Errorf("不支持算术展开 $((")
		}
		depth := 1
		j := i + 2
		for j < len(src) && depth > 0 {
			if src[j] == '(' {
				depth++
			} else if src[j] == ')' {
				depth--
			}
			j++
		}
		if depth != 0 {
			return nil, 0, fmt.Errorf("未闭合的命令替换 $(")
		}
		inner := string(src[i+2 : j-1])
		f, err := parseCommandString(inner)
		if err != nil {
			return nil, 0, err
		}
		return &CmdSubstPart{Body: f, Raw: inner}, j, nil
	case next == '{':
		// ${10} 等多位位置参数：{ 后紧跟纯数字且以 } 结尾。
		if i+2 < len(src) && isDigit(src[i+2]) {
			j := i + 2
			for j < len(src) && isDigit(src[j]) {
				j++
			}
			if j < len(src) && src[j] == '}' {
				n := 0
				for _, d := range src[i+2 : j] {
					n = n*10 + int(d-'0')
				}
				return &ParamPart{Kind: ParamPos, Index: n}, j + 1, nil
			}
		}
		// ${VAR} / ${VAR:-default}
		j := i + 2
		var name strings.Builder
		for j < len(src) && isNameRune(src[j]) {
			name.WriteRune(src[j])
			j++
		}
		if j >= len(src) || src[j] != '}' {
			// 支持 ${VAR:-def}
			if j+1 < len(src) && src[j] == ':' && src[j+1] == '-' {
				j += 2
				var def strings.Builder
				depth := 1
				for j < len(src) {
					if src[j] == '{' {
						depth++
					} else if src[j] == '}' {
						depth--
						if depth == 0 {
							break
						}
					}
					def.WriteRune(src[j])
					j++
				}
				if j < len(src) {
					j++ // 跳过 '}'
				}
				return &ParamPart{Kind: ParamVarDefault, Name: name.String(), Default: def.String()}, j, nil
			}
			return nil, 0, fmt.Errorf("未闭合的参数展开 ${")
		}
		j++ // 跳过 '}'
		return &ParamPart{Kind: ParamVar, Name: name.String()}, j, nil
	case isNameStart(next):
		j := i + 2
		for j < len(src) && isNameRune(src[j]) {
			j++
		}
		return &ParamPart{Kind: ParamVar, Name: string(src[i+1 : j])}, j, nil
	default:
		// $ 后跟其它字符（如 $1、$*、全角字符）：按字面 '$' 处理，后续字符照常。
		return &LitPart{Text: "$"}, i + 1, nil
	}
}

func isNameStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isNameRune(r rune) bool {
	return isNameStart(r) || (r >= '0' && r <= '9')
}

// parseCommandString 解析命令替换体/子命令字符串为 File。
func parseCommandString(s string) (*File, error) {
	tokens, err := newLexer(s).lex()
	if err != nil {
		return nil, err
	}
	return parse(tokens)
}
