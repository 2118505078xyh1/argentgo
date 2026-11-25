package tools

// codegraph_query 工具：基于自研代码索引查询符号的定义与引用关系，
// 并把每条命中渲染成带规则、置信度、所在上下文与源码片段的证据链。
//
// 依赖契约
// -------------------
// tools 只依赖零依赖低层包 codegraph 的公开类型（Query/Result/Stats），
// 不依赖具体实现 *codegraph.Index。具体索引由 app 在启动时构建并经由
// Registry.SetCodeGraph 注入；未注入时工具报告不可用而非返回错误。
// 该依赖不触发 archguard 的分层规则，也不会形成 import 环。

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/codegraph"
)

const (
	codegraphToolName    = "codegraph_query"
	codegraphUnavailable = "codegraph is not available"
	codegraphNoMatches   = "(no matches)"

	// codegraphDefaultLimit 是 limit 省略或非正时的默认值；
	// codegraphMaxLimit 是硬上限，防止一次查询淹没模型上下文。
	codegraphDefaultLimit = 20
	codegraphMaxLimit     = 100

	// codegraphMaxSnippetRunes 限制单条证据源码片段的 rune 数，
	// 按 rune 截断保证 CJK 不被切断 UTF-8。
	codegraphMaxSnippetRunes = 200
)

// CodeGraph 是 codegraph_query 消费的最小接口，定义在消费方（tools）。
// 具体索引 *codegraph.Index 实现它，由 app 经 SetCodeGraph 注入。
type CodeGraph interface {
	Query(q codegraph.Query) codegraph.Result
	Stats() codegraph.Stats
}

// codegraphTool 是 codegraph 查询的只读工具封装。
type codegraphTool struct {
	cg CodeGraph
}

var _ Tool = (*codegraphTool)(nil)

func (codegraphTool) Name() string { return codegraphToolName }

func (codegraphTool) Description() string {
	return "查询自研代码索引中符号的定义与引用关系，支持按包名、接收者和引用方向过滤，返回带规则与置信度的证据链。只读；工作区边界由注册表约束。"
}

func (codegraphTool) Mutating() bool { return false }

func (codegraphTool) Schema() string {
	return `{"type":"object","properties":{"name":{"type":"string","description":"要查询的符号名。"},"pkg":{"type":"string","description":"限定符号所在包路径（可选）。"},"receiver":{"type":"string","description":"限定方法接收者类型（可选）。"},"direction":{"type":"string","enum":["definitions","callers","references","all"],"description":"查询方向：definitions=定义处，callers=谁引用该符号（入边），references=该符号引用谁（出边），all=三者并集（默认）。"},"limit":{"type":"integer","description":"最多返回的边数，默认 20，硬上限 100。"}},"required":["name"]}`
}

// codegraphToolArgs 是 Run 接受的 JSON 参数。
type codegraphToolArgs struct {
	Name      string `json:"name"`
	Pkg       string `json:"pkg"`
	Receiver  string `json:"receiver"`
	Direction string `json:"direction"`
	Limit     int    `json:"limit"`
}

func (t codegraphTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	if t.cg == nil {
		return codegraphUnavailable, nil
	}

	var args codegraphToolArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("codegraph_query: bad arguments: %w", err)
	}

	// 工作区边界：把注册根解析为绝对位置并确认其仍在允许的根集合内。
	// 索引由 app 在该根上构建；这里不额外接受路径参数，故只做根校验。
	if _, err := resolve(root, "."); err != nil {
		return "", err
	}

	limit := args.Limit
	if limit <= 0 {
		limit = codegraphDefaultLimit
	}
	if limit > codegraphMaxLimit {
		limit = codegraphMaxLimit
	}
	direction := strings.TrimSpace(args.Direction)
	if direction == "" {
		direction = "all"
	}

	res := t.cg.Query(codegraph.Query{
		Name:      args.Name,
		Pkg:       args.Pkg,
		Receiver:  args.Receiver,
		Direction: direction,
		Limit:     limit,
	})

	body, sanitized, clipped := formatCodeGraphResult(res, t.cg.Stats(), limit)
	ann := AnnotateResult(codegraphToolName, body, sanitized, clipped)
	out := PrependHeader(ann.Header(), body)
	if spilled, handled := spillToolOutput(ctx, out); handled {
		return spilled, nil
	}
	return clip(ctx, out), nil
}

// Meta 把空的 codegraph_query 结果标记为无用（与 grepTool.Meta 同语义）。
func (codegraphTool) Meta(_ json.RawMessage, output string, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	if isCodeGraphNoMatches(output) {
		return ToolMeta{Useless: true}
	}
	return ToolMeta{}
}

// isCodeGraphNoMatches 通过最后一行识别空结果，兼容带来源头与不带来源头两种输出。
func isCodeGraphNoMatches(output string) bool {
	s := strings.TrimSpace(output)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	return s == codegraphNoMatches
}

// formatCodeGraphResult 把查询结果渲染为稳定文本，并返回是否发生过
// 控制字符清理与截断（供来源头标注）。
//   - 汇总行：索引文件/符号/边/命中/歧义；
//   - 每条证据：<KIND> file:line:col [rule=X conf=X] enclosing=X: snippet；
//     Definitions→DEF，Edges→CALL，Candidates→CAND；
//   - 超出 limit 的边以 "[N more omitted by limit=X]" 提示；
//   - Rules 以"规则: ..."一行解释。
func formatCodeGraphResult(res codegraph.Result, stats codegraph.Stats, limit int) (string, bool, bool) {
	sanitized, clipped := false, false
	clipped = res.Truncated > 0 // 索引侧已因 limit 截断（Truncated 为省略条数）

	hits := len(res.Definitions) + len(res.Edges) + len(res.Candidates)
	var b strings.Builder
	fmt.Fprintf(&b, "索引文件=%d 符号=%d 边=%d 命中=%d 歧义=%d",
		res.IndexedFiles, res.TotalSymbols, stats.Edges, hits, res.Ambiguous)

	type entry struct {
		kind string
		edge codegraph.Edge
	}
	entries := make([]entry, 0, hits)
	for _, e := range res.Definitions {
		entries = append(entries, entry{"DEF", e})
	}
	for _, e := range res.Edges {
		entries = append(entries, entry{"CALL", e})
	}
	for _, e := range res.Candidates {
		entries = append(entries, entry{"CAND", e})
	}

	omitted := 0
	if len(entries) > limit {
		omitted = len(entries) - limit
		entries = entries[:limit]
		clipped = true
	}

	if len(entries) == 0 && res.Truncated == 0 {
		return codegraphNoMatches, sanitized, clipped
	}

	b.WriteByte('\n')
	for _, en := range entries {
		file := en.edge.At.File
		if file == "" {
			file = en.edge.From.File
		}
		rule := sanitizeCodeGraphText(en.edge.Rule, &sanitized)
		conf := sanitizeCodeGraphText(en.edge.Conf, &sanitized)
		enclosing := sanitizeCodeGraphText(en.edge.Enclosing, &sanitized)
		snippet, cut := clipCodeGraphRunes(sanitizeCodeGraphText(en.edge.Snippet, &sanitized), codegraphMaxSnippetRunes)
		clipped = clipped || cut
		fmt.Fprintf(&b, "%s %s:%d:%d [rule=%s conf=%s] enclosing=%s: %s\n",
			en.kind, filepath.ToSlash(file), en.edge.At.Line, en.edge.At.Col,
			rule, conf, enclosing, snippet)
	}

	if omitted > 0 {
		fmt.Fprintf(&b, "[%d more omitted by limit=%d]\n", omitted, limit)
	}
	if res.Truncated > 0 {
		fmt.Fprintf(&b, "[%d more omitted by codegraph]\n", res.Truncated)
	}
	if len(res.Rules) > 0 {
		b.WriteString("规则: " + strings.Join(res.Rules, ", ") + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n"), sanitized, clipped
}

// sanitizeCodeGraphText 把控制字符（换行、制表等）替换为空格，
// 保证每条证据都是单行；非 ASCII 字符（CJK）原样保留。
func sanitizeCodeGraphText(s string, changed *bool) string {
	if s == "" {
		return s
	}
	out := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if out != s {
		*changed = true
	}
	return out
}

// clipCodeGraphRunes 按 rune 截断文本，避免切断多字节 UTF-8 字符。
func clipCodeGraphRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return s, false
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s, false
	}
	return string(runes[:max-1]) + "…", true
}
