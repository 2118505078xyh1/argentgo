package commands

// 内置 /context：模型上下文的只读可解释性视图。
//
// 本包中的命令是数据（Command 描述符加钩子结构体），
// 而非 Go 接口，因此核心逻辑位于纯构建器
// BuildContextReport 中。内置钩子（ContextHook）和 markdown 命令都
// 可以调用它，而无需本包导入 internal/app 或 internal/agent。
// 所有输入都以普通切片/字符串注入，使函数保持
// 确定性、仅依赖标准库且易于测试。

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// 边界限制防止病态输入产生无界报告（报告
// 本身可能落入它所描述的上下文中）。
const (
	maxContextItems     = 50
	maxContextNameBytes = 120
	maxContextPathBytes = 240
)

// MemoryDoc 是当前注入系统提示的一条指令文件。
// Bytes 是注入文本的长度；Reason 解释截断原因。
type MemoryDoc struct {
	Path      string
	Bytes     int
	Truncated bool
	Reason    string
}

// IgnoreStatus 是分层 .argentignore 发现的结果。
type IgnoreStatus struct {
	Files []string
	Rules int
}

// ContextInput 是渲染 /context 报告所需的一切。它只包含
// 纯数据，因此调用方拥有 app/agent 状态，本包保持
// 分层安全。
type ContextInput struct {
	MemoryDocs   []MemoryDoc
	SkillCount   int
	CommandCount int
	Tools        []string
	SandboxMode  string
	ApprovalMode string
	Ignore       IgnoreStatus
}

// contextReport 是两种渲染器共用的规范化、有界形式。
// 字段顺序固定，这也让 JSON 输出具有确定性。
type contextReport struct {
	MemoryDocs     []MemoryDoc `json:"memory_docs"`
	SkillCount     int         `json:"skill_count"`
	CommandCount   int         `json:"command_count"`
	Tools          []string    `json:"tools"`
	SandboxMode    string      `json:"sandbox_mode"`
	ApprovalMode   string      `json:"approval_mode"`
	IgnoreFiles    []string    `json:"ignore_files"`
	IgnoreRules    int         `json:"ignore_rules"`
	EstimatedBytes int         `json:"estimated_bytes"`
}

// normalizeContext 对输入排序、限界并裁剪。切片副本确保
// 调用方的切片不被改动，因此相同输入的重复调用结果稳定。
func normalizeContext(in ContextInput) contextReport {
	out := contextReport{
		SkillCount:   in.SkillCount,
		CommandCount: in.CommandCount,
		SandboxMode:  clipContextText(in.SandboxMode, maxContextNameBytes),
		ApprovalMode: clipContextText(in.ApprovalMode, maxContextNameBytes),
		IgnoreRules:  in.Ignore.Rules,
	}

	docs := append([]MemoryDoc(nil), in.MemoryDocs...)
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	for i, doc := range docs {
		if i >= maxContextItems {
			break
		}
		doc.Path = clipContextText(doc.Path, maxContextPathBytes)
		doc.Reason = clipContextText(doc.Reason, maxContextNameBytes)
		if doc.Bytes < 0 {
			doc.Bytes = 0
		}
		out.MemoryDocs = append(out.MemoryDocs, doc)
		if doc.Bytes > 0 {
			if out.EstimatedBytes > math.MaxInt-doc.Bytes {
				out.EstimatedBytes = math.MaxInt
				break
			}
			out.EstimatedBytes += doc.Bytes
		}
	}

	tools := append([]string(nil), in.Tools...)
	sort.Strings(tools)
	for i, name := range tools {
		if i >= maxContextItems {
			break
		}
		out.Tools = append(out.Tools, clipContextText(name, maxContextNameBytes))
	}

	files := append([]string(nil), in.Ignore.Files...)
	sort.Strings(files)
	for i, file := range files {
		if i >= maxContextItems {
			break
		}
		out.IgnoreFiles = append(out.IgnoreFiles, clipContextText(file, maxContextPathBytes))
	}
	return out
}

// BuildContextReport 渲染紧凑、易读的 "上下文中有什么"
// 视图。即使输入为空，每个章节也会出现。输出有界
// 且确定：相同输入总是渲染出相同文本。
func BuildContextReport(in ContextInput) string {
	r := normalizeContext(in)
	var b strings.Builder
	b.WriteString("context: read-only view of injected inputs\n")

	if len(r.MemoryDocs) == 0 {
		b.WriteString("memory docs: none\n")
	} else {
		fmt.Fprintf(&b, "memory docs (%d):\n", len(r.MemoryDocs))
		for _, doc := range r.MemoryDocs {
			status := ""
			if doc.Truncated {
				reason := doc.Reason
				if reason == "" {
					reason = "truncated"
				}
				status = fmt.Sprintf(" [truncated: %s]", reason)
			}
			fmt.Fprintf(&b, "  - %s%s (%d B)\n", doc.Path, status, doc.Bytes)
		}
	}

	fmt.Fprintf(&b, "skills: %d\n", r.SkillCount)
	fmt.Fprintf(&b, "commands: %d\n", r.CommandCount)

	if len(r.Tools) == 0 {
		b.WriteString("tools: none\n")
	} else {
		fmt.Fprintf(&b, "tools (%d): %s\n", len(r.Tools), strings.Join(r.Tools, ", "))
	}

	sandbox := r.SandboxMode
	if sandbox == "" {
		sandbox = "unspecified"
	}
	approval := r.ApprovalMode
	if approval == "" {
		approval = "unspecified"
	}
	fmt.Fprintf(&b, "sandbox: %s\n", sandbox)
	fmt.Fprintf(&b, "approval: %s\n", approval)

	if len(r.IgnoreFiles) == 0 {
		b.WriteString("ignore files: none")
	} else {
		fmt.Fprintf(&b, "ignore files (%d): %s", len(r.IgnoreFiles), strings.Join(r.IgnoreFiles, ", "))
	}
	fmt.Fprintf(&b, "; rules: %d\n", r.IgnoreRules)

	fmt.Fprintf(&b, "estimated bytes: %d\n", r.EstimatedBytes)
	return b.String()
}

// BuildContextReportJSON 是 BuildContextReport 的机器可读孪生版本。
// 它使用相同的规范化输入，因此两种视图始终一致。
func BuildContextReportJSON(in ContextInput) ([]byte, error) {
	return json.MarshalIndent(normalizeContext(in), "", "  ")
}

// clipContextText 将 s 限制在 max 字节内，且不拆分 UTF-8 rune。
func clipContextText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max - 1 // leave room for the ellipsis
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// init 把 /context 的构造器静态注册到内置命令注册表。
// 使用显式 order，确保输出顺序不依赖 init() 的执行先后。
func init() {
	builtinRegistry.MustRegister("context", builtinOrderContext, ContextCommand)
}

// ContextCommand 是内置斜杠命令描述符。注册是
// 调用方（internal/app）的职责：在 Discover 之后调用 NewSetWithBuiltins，
// 并将 `/context` 路由到 ContextHook.Run，与 /review 一致。本包
// 从不构建 app，也不为报告读取文件系统。
func ContextCommand() Command {
	return Command{
		Name:        "context",
		Description: "Show a read-only view of what is in the model context without modifying anything",
		Body:        "Report the active memory documents, skills, commands, tools, sandbox/approval mode, .argentignore status and an estimated context size. Read-only: do not modify files.",
		Path:        "<builtin:context>",
	}
}

// ContextHook 是 /context 斜杠命令的注册接缝。
// 调用方提供其拥有的 ContextInput；Run 保持纯函数且永不报错。
type ContextHook struct {
	Input ContextInput
}

// Run 实现内置命令钩子所使用的钩子签名。
func (h ContextHook) Run(_ string) (string, error) {
	return BuildContextReport(h.Input), nil
}
