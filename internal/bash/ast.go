// Package bash 实现一个纯标准库的命令解释器，目标是覆盖 bash 的日常语法子集。
//
// 它既能在无系统 shell 时兜底，也能在 opt-in 下接管多数命令。对尚未实现的
// 构造（select/子 shell/算术/后台）一律明确拒绝（fail-closed），绝不近似
// 解释——近似解析会产生解析差分，可能被注入内容利用绕过上层安全链。
//
// 分层遵循一条铁律：词法/语法阶段只构建结构，所有值展开（变量、命令替换、
// 分词、glob、去引号）都在执行期进行。若在 lexer/parser 里发现自己做了替换，
// 就是走错了层。
//
// 已原生支持：简单命令、引号、参数/命令替换、glob、管道与 && || ;、重定向、
// 赋值、if/elif/else、for、while/until、case、函数、块、break/continue/return、
// 局部作用域与位置参数。兼容矩阵见 fallback.go，与真实 bash 的差分验收见
// diff_test.go。
package bash

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
// Assigns 是命令词之前的前导赋值（NAME=VALUE）：单独出现时是赋值语句，
// 与命令同现时是该命令的临时环境变量（执行后恢复）。
type CallExpr struct {
	Args    []*Word
	Assigns []*Word
	Pos     Pos
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
	ParamPos                             // $1..$9（位置参数）
	ParamArgsAll                         // $@
	ParamArgCount                        // $#
	ParamScriptName                      // $0（脚本名，本环境恒为空）
	ParamArgsStar                        // $*（与 $@ 同义：空格连接的全部位置参数）
)

// ParamPart 是参数展开。Name 为空且 Kind==ParamLastStatus 时表示 $?。
// Default 仅在 ParamVarDefault 时有效，按字面量处理，不递归展开。
// Index 仅在 ParamPos 时有效（1..9）。
type ParamPart struct {
	Kind    ParamKind
	Name    string
	Default string
	Index   int
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
	RedirDupOut                    // 2>&1（把 stderr 指向 stdout）
	RedirAll                       // &>（stdout 与 stderr 一起重定向）
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
	case RedirAll:
		return "&>"
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

// ---- P1 控制结构节点 ----
// 这些节点让 AST 能表达 shell 的控制流。仍然遵守"语法阶段只建结构"：
// 条件、循环体、函数体都是 *Stmt（本身可再用 OpSeq 串成语句序列）。

// IfCmd 是 if cond; then body; elif cond; then body; else body; fi。
type IfCmd struct {
	Cond  *Stmt
	Then  *Stmt
	Elifs []*ElifClause
	Else  *Stmt
	Pos   Pos
}

type ElifClause struct {
	Cond *Stmt
	Body *Stmt
}

func (*IfCmd) commandNode()    {}
func (c *IfCmd) Position() Pos { return c.Pos }

// ForCmd 是 for var in words; do body; done。
type ForCmd struct {
	Var   string
	Items []*Word
	Body  *Stmt
	Pos   Pos
}

func (*ForCmd) commandNode()    {}
func (c *ForCmd) Position() Pos { return c.Pos }

// WhileCmd 表示 while/until cond; do body; done。Until=true 时条件取反。
type WhileCmd struct {
	Cond  *Stmt
	Body  *Stmt
	Until bool
	Pos   Pos
}

func (*WhileCmd) commandNode()    {}
func (c *WhileCmd) Position() Pos { return c.Pos }

// CaseCmd 是 case word in pat) body;; ... esac。
type CaseCmd struct {
	Word    *Word
	Clauses []*CaseClause
	Pos     Pos
}

type CaseClause struct {
	Patterns []*Word
	Body     *Stmt
}

func (*CaseCmd) commandNode()    {}
func (c *CaseCmd) Position() Pos { return c.Pos }

// FuncDecl 是函数定义 name() { body; } 或 function name { body; }。
type FuncDecl struct {
	Name string
	Body *Stmt
	Pos  Pos
}

func (*FuncDecl) commandNode()    {}
func (c *FuncDecl) Position() Pos { return c.Pos }

// Block 是花括号复合命令 { body; }。
type Block struct {
	Body *Stmt
	Pos  Pos
}

func (*Block) commandNode()    {}
func (c *Block) Position() Pos { return c.Pos }
