package shell

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
	left, err := p.parseAndOr()
	if err != nil {
		return nil, err
	}
	for p.cur().Kind == TokSemi {
		p.next()
		if p.cur().Kind == TokEOF {
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
	var words []*Word
	var redirs []*Redirect

	for {
		t := p.cur()
		switch t.Kind {
		case TokEOF, TokSemi, TokAnd, TokOr, TokPipe, TokRParen:
			goto done
		case TokUnsupported:
			p.next()
			return &Stmt{Cmd: &UnsupportedCmd{Kind: t.Raw, Pos: pos}, Pos: pos}, nil
		case TokRedirOut, TokRedirAppend, TokRedirIn, TokRedirDupOut:
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
			words = append(words, w)
			p.next()
		default:
			return nil, &ParseError{Pos: t.Pos, Msg: "意外的 token " + t.Kind.String()}
		}
	}
done:
	if len(words) == 0 && len(redirs) == 0 {
		return nil, &ParseError{Pos: pos, Msg: "缺少命令"}
	}
	var cmd Command
	if len(words) == 0 {
		// 只有重定向、无命令：用一个空的 CallExpr 表示（展开后程序名为空 → 报错）。
		cmd = &CallExpr{Pos: pos}
	} else {
		cmd = &CallExpr{Args: words, Pos: pos}
	}
	return &Stmt{Cmd: cmd, Redirs: redirs, Pos: pos}, nil
}

func (p *parser) parseRedirect() (*Redirect, error) {
	t := p.next() // 操作符
	r := &Redirect{Pos: t.Pos}
	switch t.Kind {
	case TokRedirOut:
		r.Op, r.N = RedirOut, 1
	case TokRedirAppend:
		r.Op, r.N = RedirAppend, 1
	case TokRedirIn:
		r.Op, r.N = RedirIn, 0
	case TokRedirDupOut:
		r.Op, r.N = RedirDupOut, 2 // 2>&1
		// 2>&1 没有目标单词。
		return r, nil
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
