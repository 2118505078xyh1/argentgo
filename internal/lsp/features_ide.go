package lsp

import (
	"bytes"
	"context"
	"encoding/json"
)

// 创建文件类资源操作不带编辑，被跳过。
// DocumentSymbol 返回 uri 中声明的符号，从服务器的 DocumentSymbol 树展平。Depth 记录嵌套（顶层为 0）。
func (c *Client) DocumentSymbol(ctx context.Context, uri string) ([]Symbol, error) {
	raw, err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	if err != nil {
		return nil, err
	}
	return parseSymbols(raw), nil
}

// DocumentSymbolAt 打开 path（若需要）并列出其符号。
func (c *Client) DocumentSymbolAt(ctx context.Context, path string) ([]Symbol, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.DocumentSymbol(ctx, uri)
}

// References 返回对 position 处符号的所有引用。
func (c *Client) References(ctx context.Context, uri string, position Position, includeDeclaration bool) ([]Location, error) {
	raw, err := c.call(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
		"context":      map[string]any{"includeDeclaration": includeDeclaration},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// ReferencesAt 打开 path（若需要）并在该处查找引用。
func (c *Client) ReferencesAt(ctx context.Context, path string, position Position, includeDeclaration bool) ([]Location, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.References(ctx, uri, position, includeDeclaration)
}

// CodeActions 返回服务器为某范围提供的动作。提供的诊断在请求上下文中转发，使快速修复能针对
// 诊断在请求上下文中转发，使快速修复能针对
// them.
func (c *Client) CodeActions(ctx context.Context, uri string, rng Range, diagnostics []Diagnostic) ([]CodeAction, error) {
	raw, err := c.call(ctx, "textDocument/codeAction", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"range":        rng,
		"context":      map[string]any{"diagnostics": lspDiagnostics(diagnostics)},
	})
	if err != nil {
		return nil, err
	}
	return parseCodeActions(raw), nil
}

// CodeActionsAt 打开 path（若需要）并为某范围提供动作。
func (c *Client) CodeActionsAt(ctx context.Context, path string, rng Range, diagnostics []Diagnostic) ([]CodeAction, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.CodeActions(ctx, uri, rng, diagnostics)
}

// Rename 向服务器请求重命名 position 处符号的编辑。
func (c *Client) Rename(ctx context.Context, uri string, position Position, newName string) (*WorkspaceEdit, error) {
	raw, err := c.call(ctx, "textDocument/rename", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
		"newName":      newName,
	})
	if err != nil {
		return nil, err
	}
	return parseWorkspaceEdit(raw), nil
}

// RenameAt 打开 path（若需要）并在那里计算重命名编辑。
func (c *Client) RenameAt(ctx context.Context, path string, position Position, newName string) (*WorkspaceEdit, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Rename(ctx, uri, position, newName)
}

// Formatting 返回格式化整个文档的编辑。
func (c *Client) Formatting(ctx context.Context, uri string, options FormattingOptions) ([]TextEdit, error) {
	raw, err := c.call(ctx, "textDocument/formatting", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"options":      options,
	})
	if err != nil {
		return nil, err
	}
	return parseTextEdits(raw), nil
}

// FormattingAt 打开 path（若需要）并返回其格式化编辑。
func (c *Client) FormattingAt(ctx context.Context, path string, options FormattingOptions) ([]TextEdit, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Formatting(ctx, uri, options)
}

// WorkspaceSymbols 在工作区中查询匹配 query 的符号。
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]SymbolInformation, error) {
	raw, err := c.call(ctx, "workspace/symbol", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	return parseSymbolInformation(raw), nil
}

// rawArray 返回 JSON 数组结果的元素，或对 null、空结果或非数组值返回 nil。
// 空结果或非数组值。
func rawArray(raw json.RawMessage) []json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || trimmed[0] != '[' {
		return nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(trimmed, &entries); err != nil {
		return nil
	}
	return entries
}

// rawDocumentSymbol 同时覆盖树状的 DocumentSymbol 和扁平的
// rawDocumentSymbol 同时覆盖树状的 DocumentSymbol 和某些服务器为 documentSymbol 返回的扁平 SymbolInformation 形态。
type rawDocumentSymbol struct {
	Name           string            `json:"name"`
	Kind           int               `json:"kind"`
	Range          *Range            `json:"range"`
	SelectionRange *Range            `json:"selectionRange"`
	Detail         string            `json:"detail"`
	Children       []json.RawMessage `json:"children"`
	Location       *Location         `json:"location"`
}

// parseSymbols 展平 textDocument/documentSymbol 结果。DocumentSymbol 的子级以递增的 Depth 递归遍历；返回 SymbolInformation 条目的服务器则按每个位置规范化。
// 子级以递增的 Depth 递归遍历；返回
// SymbolInformation 条目的服务器则按每个位置规范化。
func parseSymbols(raw json.RawMessage) []Symbol {
	entries := rawArray(raw)
	if len(entries) == 0 {
		return nil
	}
	return flattenSymbols(entries, 0)
}

func flattenSymbols(entries []json.RawMessage, depth int) []Symbol {
	out := make([]Symbol, 0, len(entries))
	for _, entry := range entries {
		var sym rawDocumentSymbol
		if err := json.Unmarshal(entry, &sym); err != nil {
			continue
		}
		if sym.Location != nil {
			// SymbolInformation 回退：有位置且无子级的叶节点。
			out = append(out, Symbol{
				Name:           sym.Name,
				Kind:           sym.Kind,
				Range:          sym.Location.Range,
				SelectionRange: sym.Location.Range,
				URI:            sym.Location.URI,
				Depth:          depth,
			})
			continue
		}
		span := Range{}
		if sym.Range != nil {
			span = *sym.Range
		}
		selection := span
		if sym.SelectionRange != nil {
			selection = *sym.SelectionRange
		}
		out = append(out, Symbol{
			Name:           sym.Name,
			Kind:           sym.Kind,
			Range:          span,
			SelectionRange: selection,
			Detail:         sym.Detail,
			Depth:          depth,
		})
		if len(sym.Children) > 0 {
			out = append(out, flattenSymbols(sym.Children, depth+1)...)
		}
	}
	return out
}

// parseSymbolInformation 解码 workspace/symbol 结果。
func parseSymbolInformation(raw json.RawMessage) []SymbolInformation {
	entries := rawArray(raw)
	if len(entries) == 0 {
		return nil
	}
	out := make([]SymbolInformation, 0, len(entries))
	for _, entry := range entries {
		var info SymbolInformation
		if err := json.Unmarshal(entry, &info); err != nil {
			continue
		}
		if info.Name == "" && info.Location.URI == "" {
			continue
		}
		out = append(out, info)
	}
	return out
}

// parseTextEdits 解码格式化结果：TextEdits 数组、单个
// parseTextEdits 解码格式化结果：TextEdits 数组、单个 TextEdit 对象或 null。
func parseTextEdits(raw json.RawMessage) []TextEdit {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] != '[' {
		var single TextEdit
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil
		}
		return []TextEdit{single}
	}
	var edits []TextEdit
	if err := json.Unmarshal(trimmed, &edits); err != nil {
		return nil
	}
	return edits
}

// parseWorkspaceEdit 解码 WorkspaceEdit，同时接受旧的
// parseWorkspaceEdit 解码 WorkspaceEdit，同时接受旧的 changes map 和 documentChanges 数组。两种形态的文本编辑被合并到 Changes，使调用方能读取单一按文件的视图。
// 合并到 Changes，使调用方能读取单一按文件的视图。
func parseWorkspaceEdit(raw json.RawMessage) *WorkspaceEdit {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" || trimmed[0] != '{' {
		return nil
	}
	var payload struct {
		Changes         map[string][]TextEdit `json:"changes"`
		DocumentChanges []json.RawMessage     `json:"documentChanges"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil
	}
	edit := &WorkspaceEdit{Changes: make(map[string][]TextEdit, len(payload.Changes))}
	for uri, edits := range payload.Changes {
		edit.Changes[uri] = append(edit.Changes[uri], edits...)
	}
	for _, rawDoc := range payload.DocumentChanges {
		var doc struct {
			Kind         string `json:"kind"`
			URI          string `json:"uri"`
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			Edits []TextEdit `json:"edits"`
		}
		if err := json.Unmarshal(rawDoc, &doc); err != nil {
			continue
		}
		if doc.Kind != "" {
			// 资源操作（创建/重命名/删除）：无文本编辑可应用。
			continue
		}
		uri := doc.TextDocument.URI
		if uri == "" {
			uri = doc.URI
		}
		if uri == "" {
			continue
		}
		edit.DocumentChanges = append(edit.DocumentChanges, DocumentEdit{URI: uri, Edits: doc.Edits})
		edit.Changes[uri] = append(edit.Changes[uri], doc.Edits...)
	}
	return edit
}

// rawCodeAction 镜像 CodeAction 的线上形态。诊断保持原始，因为其严重性在线上是数字而在 Diagnostic 中是词。
// 因为其严重性在线上是数字而在 Diagnostic 中是词。
type rawCodeAction struct {
	Title       string          `json:"title"`
	Kind        string          `json:"kind"`
	Diagnostics []rawDiagnostic `json:"diagnostics"`
	Edit        json.RawMessage `json:"edit"`
	Command     *Command        `json:"command"`
	IsPreferred bool            `json:"isPreferred"`
	Disabled    *struct {
		Reason string `json:"reason"`
	} `json:"disabled"`
}

// parseCodeActions 解码 textDocument/codeAction 结果。命令形态的旧条目通过其 Command 字段保留。
// 命令形态的旧条目通过其 Command 字段保留。
func parseCodeActions(raw json.RawMessage) []CodeAction {
	entries := rawArray(raw)
	if len(entries) == 0 {
		return nil
	}
	out := make([]CodeAction, 0, len(entries))
	for _, entry := range entries {
		var item rawCodeAction
		if err := json.Unmarshal(entry, &item); err != nil {
			continue
		}
		if item.Title == "" && item.Command == nil {
			continue
		}
		action := CodeAction{
			Title:       item.Title,
			Kind:        item.Kind,
			IsPreferred: item.IsPreferred,
			Edit:        parseWorkspaceEdit(item.Edit),
			Command:     item.Command,
		}
		if item.Disabled != nil {
			action.Disabled = &CodeActionDisabled{Reason: item.Disabled.Reason}
		}
		for _, d := range item.Diagnostics {
			action.Diagnostics = append(action.Diagnostics, Diagnostic{
				Range:    Range{Start: d.Range.Start, End: d.Range.End},
				Severity: severityName(d.Severity),
				Message:  d.Message,
				Source:   d.Source,
			})
		}
		out = append(out, action)
	}
	return out
}

// lspDiagnostics 把我们的词形严重性诊断转回代码动作请求上下文期望的数字形式。
// 代码动作请求上下文期望的数字形式。
func lspDiagnostics(items []Diagnostic) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, d := range items {
		entry := map[string]any{
			"range":    d.Range,
			"severity": severityNumber(d.Severity),
			"message":  d.Message,
		}
		if d.Source != "" {
			entry["source"] = d.Source
		}
		out = append(out, entry)
	}
	return out
}

func severityNumber(name string) int {
	switch name {
	case "error":
		return 1
	case "warning":
		return 2
	case "information":
		return 3
	case "hint":
		return 4
	default:
		return 1
	}
}
