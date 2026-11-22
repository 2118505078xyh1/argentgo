package bash

import (
	"fmt"
	"strings"
	"unicode"
)

// LexError 是词法错误（如未闭合的引号）。
type LexError struct {
	Pos Pos
	Msg string
}

func (e *LexError) Error() string {
	return fmt.Sprintf("shell: line %d col %d: %s", e.Pos.Line, e.Pos.Col, e.Msg)
}

// lexer 把一行命令切成 token 流。它只识别单词边界与操作符，不做任何
// 变量替换/去引号/glob——那些是 parser（建结构）与 interp（展开）的职责。
type lexer struct {
	runes []rune
	pos   int
	line  int
	col   int
}

func newLexer(src string) *lexer {
	return &lexer{runes: []rune(src), line: 1, col: 1}
}

func (l *lexer) cur() rune {
	if l.pos >= len(l.runes) {
		return 0
	}
	return l.runes[l.pos]
}

func (l *lexer) atEnd() bool { return l.pos >= len(l.runes) }

// peek 返回从当前位置起第 n 个 rune（0 为当前）。
func (l *lexer) peek(n int) rune {
	if l.pos+n >= len(l.runes) {
		return 0
	}
	return l.runes[l.pos+n]
}

func (l *lexer) position() Pos {
	return Pos{Offset: l.pos, Line: l.line, Col: l.col}
}

// advance 消费当前 rune 并更新行列（换行重置列）。
func (l *lexer) advance() {
	if l.atEnd() {
		return
	}
	r := l.runes[l.pos]
	l.pos++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
}

// lex 扫出全部 token。
func (l *lexer) lex() ([]Token, error) {
	// NUL 字节会让 isWordBreak(0) 立即终止 lexWord 而不推进 pos，
	// 使主循环无限追加空 token 直至 OOM。命令来自模型输出，必须在此拒绝。
	for _, r := range l.runes {
		if r == 0 {
			return nil, &LexError{Pos: l.position(), Msg: "NUL byte in command"}
		}
	}
	var tokens []Token
	for !l.atEnd() {
		pos := l.position()
		r := l.cur()
		// 换行是语句分隔符（与 ';' 等价），这是多行 shell 脚本的基础。
		if r == '\n' {
			l.advance()
			tokens = append(tokens, Token{TokSemi, "\n", pos})
			continue
		}
		if unicode.IsSpace(r) {
			l.advance()
			continue
		}
		switch r {
		case ';':
			l.advance()
			tokens = append(tokens, Token{TokSemi, ";", pos})
		case '|':
			if l.peek(1) == '|' {
				l.advance()
				l.advance()
				tokens = append(tokens, Token{TokOr, "||", pos})
			} else {
				l.advance()
				tokens = append(tokens, Token{TokPipe, "|", pos})
			}
		case '&':
			if l.peek(1) == '&' {
				l.advance()
				l.advance()
				tokens = append(tokens, Token{TokAnd, "&&", pos})
			} else if l.peek(1) == '>' {
				// &>file：stdout 与 stderr 一起重定向。
				l.advance()
				l.advance()
				tokens = append(tokens, Token{TokRedirAll, "&>", pos})
			} else {
				// 单独的 '&' 表示后台任务——不支持，明确打标。
				l.advance()
				tokens = append(tokens, Token{TokUnsupported, "&", pos})
			}
		case '>':
			if l.peek(1) == '>' {
				l.advance()
				l.advance()
				tokens = append(tokens, Token{TokRedirAppend, ">>", pos})
			} else {
				l.advance()
				tokens = append(tokens, Token{TokRedirOut, ">", pos})
			}
		case '<':
			l.advance()
			tokens = append(tokens, Token{TokRedirIn, "<", pos})
		case '(':
			// 空括号 () 是函数定义的参数表；非空括号才是子 shell（暂不支持）。
			if l.peek(1) == ')' {
				l.advance()
				l.advance()
				tokens = append(tokens, Token{TokFuncParen, "()", pos})
				continue
			}
			// 子 shell ( ... ) 不支持：把整个括号组吞成一个 Unsupported token，
			// 避免内部 token 泄漏成"意外 token"语法错误（fail-closed 但要干净）。
			start := l.pos
			l.advance()
			depth := 1
			for !l.atEnd() && depth > 0 {
				if l.cur() == '(' {
					depth++
				} else if l.cur() == ')' {
					depth--
				}
				l.advance()
			}
			raw := string(l.runes[start:l.pos])
			tokens = append(tokens, Token{TokUnsupported, raw, pos})
		case ')':
			l.advance()
			tokens = append(tokens, Token{TokRParen, ")", pos})
		case '{':
			l.advance()
			tokens = append(tokens, Token{TokLBrace, "{", pos})
		case '}':
			l.advance()
			tokens = append(tokens, Token{TokRBrace, "}", pos})
		case '#':
			// 词首的 '#' 开启 shell 注释：吞掉从 '#' 到行尾（或 EOF）的所有字符。
			// 产出 TokUnsupported 使 Supported() 返回 false，调用方据此回退系统 shell，
			// 由真实 shell 给出正确的注释语义（避免内建解析差分被反向利用来注入）。
			// 注意：只在词首触发——单词中间的 '#' 不是 word break，仍由 lexWord
			// 按字面处理（与 bash 一致）；换行本身留给 lexer 当作空白，下一行照常解析。
			for !l.atEnd() && l.cur() != '\n' {
				l.advance()
			}
			tokens = append(tokens, Token{TokUnsupported, "#", pos})
		default:
			// 数字 fd 重定向：2>file / 2>>file / 2<file，以及 2>&1。
			// fd 编码进 token.Raw 的前缀，parser 据此还原（避免给 Token 加字段）。
			if isDigit(r) && (l.peek(1) == '>' || l.peek(1) == '<') {
				fd := string(r)
				if l.peek(1) == '>' && l.peek(2) == '&' {
					l.advance() // 数字
					l.advance() // >
					l.advance() // &
					if l.cur() == '1' {
						l.advance()
						tokens = append(tokens, Token{TokRedirDupOut, fd + ">&1", pos})
					} else {
						tokens = append(tokens, Token{TokUnsupported, fd + ">&", pos})
					}
					continue
				}
				l.advance() // 数字
				if l.cur() == '>' {
					l.advance()
					if l.cur() == '>' {
						l.advance()
						tokens = append(tokens, Token{TokRedirAppend, fd + ">>", pos})
					} else {
						tokens = append(tokens, Token{TokRedirOut, fd + ">", pos})
					}
				} else { // '<'
					l.advance()
					tokens = append(tokens, Token{TokRedirIn, fd + "<", pos})
				}
				continue
			}
			tok, err := l.lexWord()
			if err != nil {
				return nil, err
			}
			tok.Pos = pos
			tokens = append(tokens, tok)
		}
	}
	tokens = append(tokens, Token{TokEOF, "", l.position()})
	return tokens, nil
}

// isWordBreak 报告 rune 是否结束一个未加引号的单词。
func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isWordBreak(r rune) bool {
	if r == 0 {
		return true
	}
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case ';', '|', '&', '>', '<', '(', ')':
		return true
	}
	return false
}

// lexWord 读取一个单词的原始文本（含引号/转义/变量符号，原样保留）。
// 引号内的元字符不会结束单词；反斜杠转义下一个 rune。
func (l *lexer) lexWord() (Token, error) {
	var b strings.Builder
	inSgl, inDbl := false, false
	substDepth := 0 // 命令替换 $(...) 的括号深度；>0 时空白与括号都不结束单词
	for !l.atEnd() {
		r := l.cur()
		if !inSgl && !inDbl && substDepth == 0 && isWordBreak(r) {
			break
		}
		switch {
		case r == '\\' && !inSgl && substDepth == 0:
			// 反斜杠转义：把反斜杠与下一个字符都收进原文，交给 parseWord 处理。
			b.WriteRune(r)
			l.advance()
			if !l.atEnd() {
				b.WriteRune(l.cur())
				l.advance()
			}
		case r == '$' && !inSgl && l.peek(1) == '(':
			// 命令替换开始：整体收进 word，parser 的 parseDollar 负责配对与递归解析。
			b.WriteRune(r)
			l.advance()
			b.WriteRune(l.cur()) // '('
			l.advance()
			substDepth++
		case r == '(' && substDepth > 0:
			b.WriteRune(r)
			l.advance()
			substDepth++
		case r == ')' && substDepth > 0:
			b.WriteRune(r)
			l.advance()
			substDepth--
		case r == '\'' && !inDbl && substDepth == 0:
			inSgl = !inSgl
			b.WriteRune(r)
			l.advance()
		case r == '"' && !inSgl && substDepth == 0:
			inDbl = !inDbl
			b.WriteRune(r)
			l.advance()
		default:
			b.WriteRune(r)
			l.advance()
		}
	}
	if inSgl {
		return Token{}, &LexError{Pos: l.position(), Msg: "未闭合的单引号"}
	}
	if inDbl {
		return Token{}, &LexError{Pos: l.position(), Msg: "未闭合的双引号"}
	}
	return Token{Kind: TokWord, Raw: b.String()}, nil
}

// lexSpecialDupOut 在 parser 中识别 "2>&1" 形式：当一个单词恰好是 "2>&1"，
// 或 "2>" 后紧跟 "&1" 时被 lexer 拆开的情况由 parser 处理。这里提供规范化辅助。
func normalizeDupOut(raw string) bool {
	// 接受 "2>&1"（无空格）与 "2>&1" 的等价紧凑写法。
	compact := strings.ReplaceAll(raw, " ", "")
	return compact == "2>&1"
}
