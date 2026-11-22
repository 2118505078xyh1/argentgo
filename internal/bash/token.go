package bash

// TokenKind 是词法 token 的种类。
type TokenKind uint8

const (
	TokEOF         TokenKind = iota
	TokWord                  // 待展开的单词（可能含引号/变量/命令替换的原始片段）
	TokSemi                  // ;
	TokAnd                   // &&
	TokOr                    // ||
	TokPipe                  // |
	TokRedirOut              // >
	TokRedirAppend           // >>
	TokRedirIn               // <
	TokRedirDupOut           // 2>&1（作为一个整体 token）
	TokRedirAll              // &> —— stdout 与 stderr 一起重定向
	TokLParen                // (
	TokRParen                // )
	TokFuncParen             // () —— 函数定义的空参数表（name() { ... }）
	TokLBrace                // { —— 复合命令/函数体的开始
	TokRBrace                // } —— 复合命令/函数体的结束
	TokUnsupported           // 识别到但不支持的构造（如 $(( 算术、( 子shell 等）
)

func (k TokenKind) String() string {
	switch k {
	case TokEOF:
		return "EOF"
	case TokWord:
		return "word"
	case TokSemi:
		return ";"
	case TokAnd:
		return "&&"
	case TokOr:
		return "||"
	case TokPipe:
		return "|"
	case TokRedirOut:
		return ">"
	case TokRedirAppend:
		return ">>"
	case TokRedirIn:
		return "<"
	case TokRedirDupOut:
		return "2>&1"
	case TokRedirAll:
		return "&>"
	case TokLParen:
		return "("
	case TokRParen:
		return ")"
	case TokFuncParen:
		return "()"
	case TokLBrace:
		return "{"
	case TokRBrace:
		return "}"
	case TokUnsupported:
		return "unsupported"
	default:
		return "?"
	}
}

// Token 是一个词法单元。Raw 是原始文本（对 TokWord 是含引号/转义/变量的原样片段），
// Pos 是起始位置。
type Token struct {
	Kind TokenKind
	Raw  string
	Pos  Pos
}
