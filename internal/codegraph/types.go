// Package codegraph 提供本地、确定性的 Go 代码图谱。
//
// 它只依赖 go/parser 等标准库：不引入向量库、不联网、不使用 cgo。
// 每条边都带"可解释来源"的证据三元组（Rule + At + Snippet），
// 因此调用方总能回答"这条关系是从哪行源码推出来的"。
package codegraph

// Symbol 是一个顶层定义。Kind 取值：func / method / type / const / var。
// File 是相对 Build root 的 slash 路径；Pkg 取源码 package 子句的名字。
type Symbol struct {
	Name, Kind, Pkg, Receiver, File string
	Line, Col                       int
}

// Ref 是一个引用目标。Pkg 与 Receiver 是可选限定词，
// 消解同名定义时带限定词的引用只匹配同限定的定义。
type Ref struct{ Name, Pkg, Receiver string }

// Pos 是源码位置，File 为相对 root 的 slash 路径。
type Pos struct {
	File      string
	Line, Col int
}

// Edge 是一条"从 From 指向 To"的可解释引用边。
// Rule 取值：call / method_call / type_ref / composite / field / definition；
// Conf 取值：exact（唯一命中）/ candidate（零命中或多命中）。
// Enclosing 是边所在的函数或类型；Snippet 是触发该边的源码行截（≤120 字节，CJK 安全）。
type Edge struct {
	From      Symbol
	To        Ref
	Rule      string
	Conf      string
	Enclosing string
	At        Pos
	Snippet   string
}

// Query 是一次图查询。Direction 取值：
// definitions（定义处）/ callers（To 命中的入边）/ references（From 命中的出边）/ all（三者并集）。
// Name/Pkg/Receiver 是可选过滤词，空字符串表示不过滤；Limit<=0 表示不截断。
type Query struct {
	Name, Pkg, Receiver, Direction string
	Limit                          int
}

// Result 是一次查询结果。Definitions 是定义记录（以 Rule=definition 的边表示）；
// Edges 是 Conf=exact 的引用边；Candidates 是 Conf=candidate 的引用边。
// Ambiguous 统计命中多个定义的 candidate 边；Truncated 是 Limit 丢弃的边数（0 表示未截断）。
// Rules 是本次结果实际用到的 Rule 标识（字典序去重）。
type Result struct {
	Definitions, Edges, Candidates                   []Edge
	Ambiguous, Truncated, IndexedFiles, TotalSymbols int
	Rules                                            []string
}

// Stats 是索引的规模摘要。
type Stats struct{ Files, Symbols, Edges int }

// Index 是一次 Build 产出的不可变快照；并发只读安全。
type Index struct {
	root   string
	files  map[string]*fileData // key 为 slash 相对路径
	defs   []Symbol             // 按 (File, Line, Name) 排序
	edges  []Edge               // 已消解并按 (At.File, At.Line, To.Name) 排序
	byName map[string][]Symbol  // 消解用的名字索引
}

// fileStamp 是文件版本的廉价指纹（mtime+size），用于增量复用。
type fileStamp struct {
	modTime int64
	size    int64
}

// fileData 是单个 .go 文件的解析结果；edges 尚未经过全局符号消解，
// Conf 在 Index.finalize 中统一填写，保证增量与全量构建行为一致。
type fileData struct {
	symbols []Symbol
	edges   []Edge
	lines   []string
	stamp   fileStamp
}
