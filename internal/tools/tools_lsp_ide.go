package tools

// LSP IDE 能力工具。它们把为 IDE 支持添加的语言服务器方法
// （文档/工作区符号、引用、代码操作、
// 重命名和格式化）作为只读工具暴露给模型。
//
// 依赖契约
// -------------------
// internal/tools 不得导入 internal/lsp（archguard 测试强制
// 执行核心分层），也不得导入它的类型。相反，本文件拥有
// 镜像对应 lsp 类型 JSON 形状的小型中立 DTO，
// 以及一个消费-side 接口（LspIde），其方法返回这些 DTO。
//
// 接线：internal/app 必须注入一个包装 *lsp.Client 的适配器，调用
// 带类型的 IDE 方法，并把结果转换成下面的中立 DTO。
// 该适配器通过 SetLspIde 安装到 Registry 上。在安装之前，
// 六个工具仍会在 Specs 中公布，但报告
// "LSP IDE integration is not available" 而不返回错误。
//
// 该适配器具有如下精确形状（lsp 类型逐字段转换）：
//
//	type lspIdeAdapter struct{ client *lsp.Client }
//
//	func (a *lspIdeAdapter) DocumentSymbol(path string) ([]tools.LspSymbol, error) {
//	    items, err := a.client.DocumentSymbolAt(context.Background(), path)
//	    // convert each lsp.Symbol -> tools.LspSymbol (Kind is the numeric LSP kind)
//	}
//	func (a *lspIdeAdapter) References(path string, line, character int, includeDecl bool) ([]tools.LspLocation, error) {
//	    a.client.ReferencesAt(ctx, path, lsp.Position{Line: line, Character: character}, includeDecl)
//	}
//	func (a *lspIdeAdapter) CodeActions(path string, startLine, startChar, endLine, endChar int) ([]tools.LspCodeAction, error) {
//	    rng := lsp.Range{Start: lsp.Position{Line: startLine, Character: startChar}, End: lsp.Position{Line: endLine, Character: endChar}}
//	    a.client.CodeActionsAt(ctx, path, rng, nil)
//	}
//	func (a *lspIdeAdapter) Rename(path string, line, char int, newName string) (*tools.LspWorkspaceEdit, error) {
//	    a.client.RenameAt(ctx, path, lsp.Position{Line: line, Character: char}, newName)
//	}
//	func (a *lspIdeAdapter) Formatting(path string, tabSize int, insertSpaces bool) ([]tools.LspTextEdit, error) {
//	    a.client.FormattingAt(ctx, path, lsp.FormattingOptions{TabSize: tabSize, InsertSpaces: insertSpaces})
//	}
//	func (a *lspIdeAdapter) WorkspaceSymbols(query string) ([]tools.LspSymbolInfo, error) {
//	    a.client.WorkspaceSymbols(ctx, query)
//	}
//
// 六个工具全部是只读预览：rename 和 formatting 把
// 服务器提议的编辑作为文本返回，从不应用它们。实际修改
// 由普通的 edit/write 工具完成。因此这里每个工具的 Mutating() 都是 false
// 且在只读沙箱中保持可用。

import (
	"context"
	"encoding/json"
	"fmt"
)

// 当尚未接线适配器时返回 lspIdeUnavailable（不返回错误）。
// 保持它是一条优雅的消息，让模型看到该能力
// 存在，同时在没有语言服务器的环境中不使回合失败。
const lspIdeUnavailable = "LSP IDE integration is not available"

// ---- 镜像 lsp 类型 JSON 形状的中立 DTO ----

// LspPos 是基于 0 的 LSP 位置（行和 UTF-16 字符偏移）。
type LspPos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LspRange 是两个基于 0 的位置之间的跨度。
type LspRange struct {
	Start LspPos `json:"start"`
	End   LspPos `json:"end"`
}

// LspLocation 是定义/引用目标。
type LspLocation struct {
	URI   string   `json:"uri"`
	Range LspRange `json:"range"`
}

// LspSymbol 是一条扁平的文档符号条目。Kind 镜像数字的 LSP
// 符号类别；Depth 由客户端推导，不属于传输 JSON。
type LspSymbol struct {
	Name           string   `json:"name"`
	Kind           int      `json:"kind"`
	Range          LspRange `json:"range"`
	SelectionRange LspRange `json:"selectionRange"`
	URI            string   `json:"uri,omitempty"`
	Detail         string   `json:"detail,omitempty"`
	Depth          int      `json:"-"`
}

// LspTextEdit 是用新文本替换某段文档范围的一次操作。
type LspTextEdit struct {
	Range   LspRange `json:"range"`
	NewText string   `json:"newText"`
}

// LspDocumentEdit 是来自 WorkspaceEdit.documentChanges 的某个文件的编辑。
type LspDocumentEdit struct {
	URI   string        `json:"uri"`
	Edits []LspTextEdit `json:"edits"`
}

// LspWorkspaceEdit 按文件 URI 分组文本编辑。
type LspWorkspaceEdit struct {
	Changes         map[string][]LspTextEdit `json:"changes,omitempty"`
	DocumentChanges []LspDocumentEdit        `json:"documentChanges,omitempty"`
}

// LspDiagnostic 是附着在代码操作上的一个问题。
type LspDiagnostic struct {
	Range    LspRange `json:"range"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Source   string   `json:"source,omitempty"`
}

// LspCommand 是代码操作携带的命令引用。参数保持
// 为 `any`，因为其形状由服务器定义；这与 lsp.Command 一致，
// 使 app 适配器可以直接赋值该切片。
type LspCommand struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// LspCodeActionDisabled 解释某项操作为何不可用。
type LspCodeActionDisabled struct {
	Reason string `json:"reason"`
}

// LspCodeAction 是服务器针对某范围提供的一个修复或重构。
type LspCodeAction struct {
	Title       string                 `json:"title"`
	Kind        string                 `json:"kind,omitempty"`
	Diagnostics []LspDiagnostic        `json:"diagnostics,omitempty"`
	Edit        *LspWorkspaceEdit      `json:"edit,omitempty"`
	Command     *LspCommand            `json:"command,omitempty"`
	IsPreferred bool                   `json:"isPreferred,omitempty"`
	Disabled    *LspCodeActionDisabled `json:"disabled,omitempty"`
}

// LspSymbolInfo 是工作区符号查询中的一条扁平条目。
type LspSymbolInfo struct {
	Name          string      `json:"name"`
	Kind          int         `json:"kind"`
	Location      LspLocation `json:"location"`
	ContainerName string      `json:"containerName,omitempty"`
}

// LspIde 是由 app 提供的、包装 *lsp.Client 的适配器所满足的
// 消费侧接口。所有位置都是基于 0 的 LSP 坐标；工具
// 原样透传它们。
type LspIde interface {
	DocumentSymbol(path string) ([]LspSymbol, error)
	References(path string, line, character int, includeDecl bool) ([]LspLocation, error)
	CodeActions(path string, startLine, startChar, endLine, endChar int) ([]LspCodeAction, error)
	Rename(path string, line, char int, newName string) (*LspWorkspaceEdit, error)
	Formatting(path string, tabSize int, insertSpaces bool) ([]LspTextEdit, error)
	WorkspaceSymbols(query string) ([]LspSymbolInfo, error)
}

// lspIdeTool 是单个通用只读工具。每个实例携带自己的
// 适配器和一个调用闭包，该闭包解码参数并调用某个 LspIde
// 方法，在保持每个工具元数据的同时让六个工具 DRY。
type lspIdeTool struct {
	name        string
	description string
	schema      string
	ide         LspIde
	call        func(ctx context.Context, ide LspIde, raw json.RawMessage) (any, error)
}

func (t *lspIdeTool) Name() string        { return t.name }
func (t *lspIdeTool) Description() string { return t.description }
func (t *lspIdeTool) Schema() string      { return t.schema }
func (t *lspIdeTool) Mutating() bool      { return false }

func (t *lspIdeTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
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

// ---- 参数解码器 ----

type lspPathArgs struct {
	Path string `json:"path"`
}

type lspReferenceArgs struct {
	Path               string `json:"path"`
	Line               int    `json:"line"`
	Character          int    `json:"character"`
	IncludeDeclaration bool   `json:"includeDeclaration"`
}

type lspRangeArgs struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	StartChar int    `json:"startChar"`
	EndLine   int    `json:"endLine"`
	EndChar   int    `json:"endChar"`
}

type lspRenameArgs struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	NewName   string `json:"newName"`
}

type lspFormatArgs struct {
	Path         string `json:"path"`
	TabSize      int    `json:"tabSize"`
	InsertSpaces *bool  `json:"insertSpaces"`
}

type lspQueryArgs struct {
	Query string `json:"query"`
}

func decodeLspArgs[T any](raw json.RawMessage) (T, error) {
	var args T
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("bad arguments: %w", err)
	}
	return args, nil
}

// ---- 构造器 ----

func newLspSymbolsTool(ide LspIde) Tool {
	return &lspIdeTool{
		name:        "lsp_symbols",
		description: "List the symbols declared in a document (functions, types, methods, variables), flattened with nesting depth. Requires the LSP IDE adapter to be wired.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"}},"required":["path"]}`,
		ide:         ide,
		call: func(_ context.Context, ide LspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspPathArgs](raw)
			if err != nil {
				return nil, err
			}
			return ide.DocumentSymbol(args.Path)
		},
	}
}

func newLspReferencesTool(ide LspIde) Tool {
	return &lspIdeTool{
		name:        "lsp_references",
		description: "Find all references to the symbol at a zero-based position, optionally including its declaration. Requires the LSP IDE adapter to be wired.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"},"line":{"type":"integer","description":"0-based line of the symbol"},"character":{"type":"integer","description":"0-based UTF-16 character offset within the line"},"includeDeclaration":{"type":"boolean","description":"include the declaration itself in the results"}},"required":["path","line","character"]}`,
		ide:         ide,
		call: func(_ context.Context, ide LspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspReferenceArgs](raw)
			if err != nil {
				return nil, err
			}
			return ide.References(args.Path, args.Line, args.Character, args.IncludeDeclaration)
		},
	}
}

func newLspCodeActionsTool(ide LspIde) Tool {
	return &lspIdeTool{
		name:        "lsp_code_actions",
		description: "List quick fixes and refactorings the language server offers for a zero-based range. Returns action titles and any proposed edits; it does not apply them.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"},"startLine":{"type":"integer","description":"0-based start line"},"startChar":{"type":"integer","description":"0-based start character"},"endLine":{"type":"integer","description":"0-based end line"},"endChar":{"type":"integer","description":"0-based end character"}},"required":["path","startLine","startChar","endLine","endChar"]}`,
		ide:         ide,
		call: func(_ context.Context, ide LspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspRangeArgs](raw)
			if err != nil {
				return nil, err
			}
			return ide.CodeActions(args.Path, args.StartLine, args.StartChar, args.EndLine, args.EndChar)
		},
	}
}

func newLspRenamePreviewTool(ide LspIde) Tool {
	return &lspIdeTool{
		name:        "lsp_rename_preview",
		description: "Preview the workspace edits that would rename the symbol at a zero-based position. Read-only: returns the proposed edits as text and never applies them; use the edit tools to apply a rename.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"},"line":{"type":"integer","description":"0-based line of the symbol"},"character":{"type":"integer","description":"0-based UTF-16 character offset within the line"},"newName":{"type":"string","description":"the replacement symbol name"}},"required":["path","line","character","newName"]}`,
		ide:         ide,
		call: func(_ context.Context, ide LspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspRenameArgs](raw)
			if err != nil {
				return nil, err
			}
			return ide.Rename(args.Path, args.Line, args.Character, args.NewName)
		},
	}
}

func newLspFormatTool(ide LspIde) Tool {
	return &lspIdeTool{
		name:        "lsp_format",
		description: "Preview the text edits that would format a document. Read-only: returns the proposed edits as text and never applies them; use the edit tools to apply formatting.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative or absolute path to the document"},"tabSize":{"type":"integer","description":"spaces per tab, default 4"},"insertSpaces":{"type":"boolean","description":"use spaces instead of tabs, default true"}},"required":["path"]}`,
		ide:         ide,
		call: func(_ context.Context, ide LspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspFormatArgs](raw)
			if err != nil {
				return nil, err
			}
			tabSize := args.TabSize
			if tabSize <= 0 {
				tabSize = 4
			}
			insertSpaces := true
			if args.InsertSpaces != nil {
				insertSpaces = *args.InsertSpaces
			}
			return ide.Formatting(args.Path, tabSize, insertSpaces)
		},
	}
}

func newLspWorkspaceSymbolsTool(ide LspIde) Tool {
	return &lspIdeTool{
		name:        "lsp_workspace_symbols",
		description: "Search the whole workspace for symbols matching a query. Returns names, kinds and locations. Requires the LSP IDE adapter to be wired.",
		schema:      `{"type":"object","properties":{"query":{"type":"string","description":"symbol name query; empty returns a broad set"}},"required":["query"]}`,
		ide:         ide,
		call: func(_ context.Context, ide LspIde, raw json.RawMessage) (any, error) {
			args, err := decodeLspArgs[lspQueryArgs](raw)
			if err != nil {
				return nil, err
			}
			return ide.WorkspaceSymbols(args.Query)
		},
	}
}
