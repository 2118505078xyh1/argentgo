package tools

// LSP 高级特性工具：semantic tokens、inlay hints 与 code lens。
// 它们把语言服务器的"渲染增强"能力作为只读工具暴露给模型，
// 让模型能解释编辑器看到的高亮、内联注解与可点击标题。
//
// 依赖契约（与 tools_lsp_ide.go / lsp_will_rename.go 一致）
// -------------------
// internal/tools 不得 import internal/lsp。本文件拥有镜像
// lsp 类型 JSON 形状的中立 DTO，以及消费侧窄接口 advancedLspIde。
// app 负责安装适配器：把 *lsp.Client 的 SemanticTokensAt /
// InlayHintsAt / CodeLensAt 结果逐字段转换成下面的 DTO，再通过
// SetLspAdvanced 注入到 Registry。
//
// 三个工具默认不出现在 New() 的清单里（golden 不变）。必须先
// 显式调用 RegisterLspAdvanced 追加到末尾，再注入适配器；
// 未注入时它们报告 lspIdeUnavailable 而非返回错误。
// 它们全部只读（Mutating=false），可在只读沙箱中使用。

import (
	"context"
	"encoding/json"
	"fmt"
)

// ---- 镜像 lsp 高级类型 JSON 形状的中立 DTO ----

// LspSemanticToken 是解码后的一条语义 token。
// LSP 线上数据是每 5 个 uint32 一组的增量流（deltaLine、deltaStart、
// length、tokenType、tokenModifiers）；适配器把它展开成绝对路径后
// 填入本结构，因此 Line/Char 是从 0 开始的绝对坐标。
type LspSemanticToken struct {
	Line      int `json:"line"`
	Char      int `json:"char"`
	Length    int `json:"length"`
	TokenType int `json:"tokenType"`
	Modifiers int `json:"modifiers"`
}

// LspInlayHint 是服务器为某位置提供的一条内联注解。
// Line/Char 为 0 基位置；Kind 是适配器把数字 LSP kind
// 映射后的可读名称（如 "type"、"parameter"）。
type LspInlayHint struct {
	Line  int    `json:"line"`
	Char  int    `json:"char"`
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
}

// LspCodeLens 是附加到某区间的可点击标签。区间为 0 基；
// Title 是展示文案，Command 是点击后执行的命令标识（若服务器提供）。
type LspCodeLens struct {
	StartLine int    `json:"startLine"`
	StartChar int    `json:"startChar"`
	EndLine   int    `json:"endLine"`
	EndChar   int    `json:"endChar"`
	Title     string `json:"title"`
	Command   string `json:"command,omitempty"`
}

// advancedLspIde 是三个高级工具需要的能力集合，由 app 适配器实现。
// 接口定义在消费方，避免 tools import app/lsp 形成环。
// 所有坐标均为 0 基 LSP 坐标，工具原样透传，不做 1 基转换。
type advancedLspIde interface {
	SemanticTokens(path string) ([]LspSemanticToken, error)
	InlayHints(path string, line, char int) ([]LspInlayHint, error)
	CodeLens(path string) ([]LspCodeLens, error)
}

// ---- 通用只读工具（镜像 lspIdeTool 的范式，但绑定 advancedLspIde）----

// advancedLspTool 是单个通用只读工具。每个实例携带自己的适配器
// 与调用闭包，使三个工具在保持各自元数据的同时共享执行骨架。
type advancedLspTool struct {
	name        string
	description string
	schema      string
	ide         advancedLspIde
	call        func(ctx context.Context, ide advancedLspIde, raw json.RawMessage) (any, error)
}

func (t *advancedLspTool) Name() string        { return t.name }
func (t *advancedLspTool) Description() string { return t.description }
func (t *advancedLspTool) Schema() string      { return t.schema }
func (t *advancedLspTool) Mutating() bool      { return false }

func (t *advancedLspTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	if t.ide == nil {
		return lspIdeUnavailable, nil
	}
	result, err := t.call(ctx, t.ide, raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", t.name, err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("%s: encode result: %w", t.name, err)
	}
	return clip(ctx, string(data)), nil
}

// ---- 参数解码 ----

type lspAdvancedPathArgs struct {
	Path string `json:"path"`
}

type lspInlayHintArgs struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Char int    `json:"char"`
}

// requireLspPath 在调用适配器前校验 path，给模型一个可操作的错误。
func requireLspPath(tool, path string) error {
	if path == "" {
		return fmt.Errorf("%s: path is required", tool)
	}
	return nil
}

// ---- 构造器 ----

const (
	lspSemanticTokensName = "lsp_semantic_tokens"
	lspInlayHintsName     = "lsp_inlay_hints"
	lspCodeLensName       = "lsp_code_lens"
)

// lspAdvancedNames 是三个工具注册/注入时的稳定顺序。
var lspAdvancedNames = [...]string{lspSemanticTokensName, lspInlayHintsName, lspCodeLensName}

func newLspSemanticTokensTool(ide advancedLspIde) Tool {
	return &advancedLspTool{
		name:        lspSemanticTokensName,
		description: "List semantic tokens the language server highlights in a document (decoded to absolute positions, with token type and modifiers). Requires the LSP advanced adapter to be wired.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"}},"required":["path"]}`,
		ide:         ide,
		call: func(_ context.Context, ide advancedLspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspAdvancedPathArgs](raw)
			if err != nil {
				return nil, err
			}
			if err := requireLspPath(lspSemanticTokensName, args.Path); err != nil {
				return nil, err
			}
			return ide.SemanticTokens(args.Path)
		},
	}
}

func newLspInlayHintsTool(ide advancedLspIde) Tool {
	return &advancedLspTool{
		name:        lspInlayHintsName,
		description: "List inlay hints (inline annotations such as inferred types or parameter names) the language server offers near an optional zero-based position. Omitted line/char default to 0. Requires the LSP advanced adapter to be wired.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"},"line":{"type":"integer","description":"optional 0-based line of the position (default 0)"},"char":{"type":"integer","description":"optional 0-based UTF-16 character offset within the line (default 0)"}},"required":["path"]}`,
		ide:         ide,
		call: func(_ context.Context, ide advancedLspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspInlayHintArgs](raw)
			if err != nil {
				return nil, err
			}
			if err := requireLspPath(lspInlayHintsName, args.Path); err != nil {
				return nil, err
			}
			return ide.InlayHints(args.Path, args.Line, args.Char)
		},
	}
}

func newLspCodeLensTool(ide advancedLspIde) Tool {
	return &advancedLspTool{
		name:        lspCodeLensName,
		description: "List code lens labels (clickable titles above declarations, often test/run links) the language server attaches to a document. Requires the LSP advanced adapter to be wired.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"}},"required":["path"]}`,
		ide:         ide,
		call: func(_ context.Context, ide advancedLspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspAdvancedPathArgs](raw)
			if err != nil {
				return nil, err
			}
			if err := requireLspPath(lspCodeLensName, args.Path); err != nil {
				return nil, err
			}
			return ide.CodeLens(args.Path)
		},
	}
}

// advancedToolOf 在工具已被 intentSchemaTool 包装时取出底层实现，
// 使 SetLspAdvanced 在 wrapSchemas 前后都能原位置替换适配器。
func advancedToolOf(t Tool) *advancedLspTool {
	switch v := t.(type) {
	case *advancedLspTool:
		return v
	case intentSchemaTool:
		if inner, ok := v.Tool.(*advancedLspTool); ok {
			return inner
		}
	}
	return nil
}

// RegisterLspAdvanced 幂等地把三个高级工具追加到注册表末尾。
// 先于注入调用时工具已可见，但报告"集成不可用"。nil 注册表为空操作。
func RegisterLspAdvanced(r *Registry) {
	if r == nil {
		return
	}
	for _, name := range lspAdvancedNames {
		if _, exists := r.tools[name]; exists {
			continue
		}
		r.tools[name] = newAdvancedToolByName(name, nil)
		r.order = append(r.order, name)
	}
}

// newAdvancedToolByName 按名字构造尚未接线的工具。
func newAdvancedToolByName(name string, ide advancedLspIde) Tool {
	switch name {
	case lspSemanticTokensName:
		return newLspSemanticTokensTool(ide)
	case lspInlayHintsName:
		return newLspInlayHintsTool(ide)
	case lspCodeLensName:
		return newLspCodeLensTool(ide)
	default:
		return nil
	}
}

// SetLspAdvanced 把适配器注入到三个工具，遵循 SetLspWillRename 的
// 原位置重建模式：工具已注册时只替换 ide 字段，不触动顺序；
// 未注册时补建并追加（因此注入本身也是幂等的）。
// 参数用 any 承载，使 tools 包不必 import app；类型断言失败时
// 工具保持不可用。
func (r *Registry) SetLspAdvanced(adapter any) {
	if r == nil {
		return
	}
	ide, _ := adapter.(advancedLspIde)
	for _, name := range lspAdvancedNames {
		if existing := advancedToolOf(r.tools[name]); existing != nil {
			existing.ide = ide
			continue
		}
		if _, exists := r.tools[name]; !exists {
			r.order = append(r.order, name)
		}
		r.tools[name] = newAdvancedToolByName(name, ide)
	}
}
