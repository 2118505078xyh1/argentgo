// Package shell 实现一个极简的、纯标准库的命令解释器。
//
// 它不是 bash 的语义替代，而是"无系统 shell 时的执行能力兜底"：只实现有把握的
// 语法子集，遇到不支持的构造一律明确拒绝（fail-closed），绝不近似解释——
// 近似解析会产生解析差分，可能被注入内容利用绕过上层安全链。
//
// 分层遵循一条铁律：词法/语法阶段只构建结构，所有值展开（变量、命令替换、
// 分词、glob、去引号）都在执行期进行。若在 lexer/parser 里发现自己做了替换，
// 就是走错了层。
package shell

// Pos 是源码位置（偏移、行、列），用于精确诊断。列从 1 开始，按 rune 计数。
type Pos struct {
	Offset int
	Line   int
	Col    int
}

// File 是解析结果：一条命令序列。
type File struct {
	Stmt *Stmt
}

// Stmt 是一条语句：一个命令 + 该命令的重定向列表。
type Stmt struct {
	Cmd    Command
	Redirs []*Redirect
	Pos    Pos
	Semi   bool // 语句以 ';' 结尾（仅用于忠实回放，不影响语义）
}

// Command 是命令节点接口。极简子集只有三种实现：
//   - *CallExpr       简单命令（程序名 + 参数）
//   - *BinaryCmd      逻辑与/或、管道
//   - *UnsupportedCmd 明确不支持的构造（解析期打标，执行期报错）
type Command interface {
	commandNode()
	Position() Pos
}

// CallExpr 是简单命令：Args 是待展开的单词序列。
// 第一个单词经展开后作为程序名，其余为参数。
type CallExpr struct {
	Args []*Word
	Pos  Pos
}

func (*CallExpr) commandNode()    {}
func (c *CallExpr) Position() Pos { return c.Pos }

// BinaryOp 是二元命令的操作符。
type BinaryOp uint8

const (
	OpAnd  BinaryOp = iota + 1 // &&
	OpOr                       // ||
	OpPipe                     // |
	OpSeq                      // ; 顺序执行（无论前一个成败）
)

func (o BinaryOp) String() string {
	switch o {
	case OpAnd:
		return "&&"
	case OpOr:
		return "||"
	case OpPipe:
		return "|"
	case OpSeq:
		return ";"
	default:
		return "?"
	}
}

// BinaryCmd 表示 X && Y / X || Y / X | Y。X、Y 都是语句（可带各自的重定向）。
type BinaryCmd struct {
	Op  BinaryOp
	X   *Stmt
	Y   *Stmt
	Pos Pos
}

func (*BinaryCmd) commandNode()    {}
func (c *BinaryCmd) Position() Pos { return c.Pos }

// UnsupportedCmd 记录一个"能识别但拒绝执行"的构造（如循环、函数、命令替换体等）。
// 它让 fail-closed 有明确载体：执行器遇到它就返回 ErrUnsupported。
type UnsupportedCmd struct {
	Kind string // 构造名，如 "for", "while", "case", "function", "subshell", "arith"
	Pos  Pos
}

func (*UnsupportedCmd) commandNode()    {}
func (c *UnsupportedCmd) Position() Pos { return c.Pos }

// Word 是一个待展开的单词，由若干 WordPart 组成。
type Word struct {
	Parts []WordPart
	Pos   Pos
}

// WordPart 是单词的一个组成部分。极简子集只需字面量、单引号、双引号、
// 参数展开（$VAR / $? / ${VAR:-def}）和命令替换（$(cmd)）。
type WordPart interface {
	wordPartNode()
}

// LitPart 是未加引号的字面文本。
type LitPart struct {
	Text string
}

func (*LitPart) wordPartNode() {}

// SglQuotedPart 是单引号内的文本（完全不展开）。
type SglQuotedPart struct {
	Text string
}

func (*SglQuotedPart) wordPartNode() {}

// DblQuotedPart 是双引号内的部分序列（展开但不分词、不 glob）。
type DblQuotedPart struct {
	Parts []WordPart
}

func (*DblQuotedPart) wordPartNode() {}

// ParamKind 区分参数展开的种类。
type ParamKind uint8

const (
	ParamVar        ParamKind = iota + 1 // $VAR / ${VAR}
	ParamLastStatus                      // $?
	ParamVarDefault                      // ${VAR:-default}
)

// ParamPart 是参数展开。Name 为空且 Kind==ParamLastStatus 时表示 $?。
// Default 仅在 ParamVarDefault 时有效，按字面量处理，不递归展开。
type ParamPart struct {
	Kind    ParamKind
	Name    string
	Default string
}

func (*ParamPart) wordPartNode() {}

// CmdSubstPart 是命令替换 $(cmd)。Body 是内部命令序列，Execution 在展开期执行。
type CmdSubstPart struct {
	Body *File
	// Raw 保留原始命令文本，便于无 File 时的诊断；执行期优先用 Body。
	Raw string
}

func (*CmdSubstPart) wordPartNode() {}

// RedirOp 是重定向操作符。
type RedirOp uint8

const (
	RedirOut    RedirOp = iota + 1 // >
	RedirAppend                    // >>
	RedirIn                        // <
	RedirDupOut                    // 2>&1（把 fd N 指向 stdout）
)

func (o RedirOp) String() string {
	switch o {
	case RedirOut:
		return ">"
	case RedirAppend:
		return ">>"
	case RedirIn:
		return "<"
	case RedirDupOut:
		return ">&"
	default:
		return "?"
	}
}

// Redirect 是一条重定向。N 是文件描述符（默认 0 或 1，2>&1 时为 2），
// Target 是待展开的目标单词（重定向目标不分词、不 glob）。
type Redirect struct {
	Op     RedirOp
	N      int
	Target *Word
	Pos    Pos
}
