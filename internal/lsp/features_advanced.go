package lsp

import (
	"bytes"
	"context"
	"encoding/json"
)

// SemanticTokens 向服务器请求 uri 的完整语义 token 流。null 结果不是错误：它产生 nil token。
// null 结果不是错误：它产生 nil token。
func (c *Client) SemanticTokens(ctx context.Context, uri string) (*SemanticTokens, error) {
	raw, err := c.call(ctx, "textDocument/semanticTokens/full", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	if err != nil {
		return nil, err
	}
	return parseSemanticTokens(raw), nil
}

// SemanticTokensAt 打开 path（若需要）并返回其语义 token。
func (c *Client) SemanticTokensAt(ctx context.Context, path string) (*SemanticTokens, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.SemanticTokens(ctx, uri)
}

// InlayHints 返回服务器在 rng 内提供的内联注解。
func (c *Client) InlayHints(ctx context.Context, uri string, rng Range) ([]InlayHint, error) {
	raw, err := c.call(ctx, "textDocument/inlayHint", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"range":        rng,
	})
	if err != nil {
		return nil, err
	}
	return parseInlayHints(raw), nil
}

// InlayHintsAt 打开 path（若需要）并列出其在 rng 内的内联提示。
func (c *Client) InlayHintsAt(ctx context.Context, path string, rng Range) ([]InlayHint, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.InlayHints(ctx, uri, rng)
}

// CodeLens 列出服务器附加到 uri 的可点击标签。
func (c *Client) CodeLens(ctx context.Context, uri string) ([]CodeLens, error) {
	raw, err := c.call(ctx, "textDocument/codeLens", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	if err != nil {
		return nil, err
	}
	return parseCodeLens(raw), nil
}

// CodeLensAt 打开 path（若需要）并列出其 code lens。
func (c *Client) CodeLensAt(ctx context.Context, path string) ([]CodeLens, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.CodeLens(ctx, uri)
}

// ResolveCodeLens 填补缺少 command 的 lens 的命令。协议把 lens 本身作为参数；返回的副本总被标记为已解析。
// 协议把 lens 本身作为参数；返回的副本总被标记为已解析。
// 服务器的不透明 "data" 不被 CodeLens 保留，因此需要它来解析 lens 的服务器无法通过此辅助函数服务。
// 解析 lens 的服务器无法通过此辅助函数服务。
func (c *Client) ResolveCodeLens(ctx context.Context, lens CodeLens) (*CodeLens, error) {
	params := map[string]any{"range": lens.Range}
	if lens.Command != nil {
		params["command"] = lens.Command
	}
	raw, err := c.call(ctx, "codeLens/resolve", params)
	if err != nil {
		return nil, err
	}
	resolved := parseCodeLens(raw)
	if len(resolved) == 0 {
		return nil, nil
	}
	resolved[0].IsResolved = true
	return &resolved[0], nil
}

// advancedRawEntries 返回 JSON 数组结果的元素，或对单个对象结果返回单元素切片。null、空、畸形和标量结果产生 nil。它让下面的解析器接受宽松的服务器
// 或对单个对象结果返回单元素切片。null、空、畸形和
// 标量结果产生 nil。它让下面的解析器接受宽松的服务器
// 而无需更改 features_ide.go 中的共享辅助函数。
func advancedRawEntries(raw json.RawMessage) []json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	switch trimmed[0] {
	case '[':
		var entries []json.RawMessage
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil
		}
		return entries
	case '{':
		// 返回前验证：单独的畸形对象不得被
		// 返回前验证：单独的畸形对象不得被误认为单个（空）结果。
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return nil
		}
		return []json.RawMessage{trimmed}
	default:
		return nil
	}
}

// parseSemanticTokens 解码 textDocument/semanticTokens/full 结果。结果是一个 SemanticTokens 对象；若服务器把它包在数组中，则使用第一个对象。Data 原样保留：每 token 五整数的流
// 结果是一个 SemanticTokens 对象；若服务器把它包在数组中，
// 则使用第一个对象。Data 原样保留：每 token 五整数的流
// 是增量编码的，只有 UI 知道如何把它还原成行。
func parseSemanticTokens(raw json.RawMessage) *SemanticTokens {
	entries := advancedRawEntries(raw)
	if len(entries) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(entries[0])
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var payload struct {
		ResultID string          `json:"resultId"`
		Data     json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil
	}
	tokens := &SemanticTokens{ResultID: payload.ResultID}
	data := bytes.TrimSpace(payload.Data)
	if len(data) > 0 && data[0] == '[' {
		var values []uint32
		if err := json.Unmarshal(data, &values); err == nil {
			tokens.Data = values
		}
	}
	return tokens
}

// rawInlayHint 镜像线上形态。Label 和 tooltip 保持原始，因为它们各可能是字符串、MarkupContent 对象或部件数组。
// 因为它们各可能是字符串、MarkupContent 对象或部件数组。
type rawInlayHint struct {
	Position     *Position       `json:"position"`
	Label        json.RawMessage `json:"label"`
	Kind         int             `json:"kind"`
	Tooltip      json.RawMessage `json:"tooltip"`
	PaddingLeft  bool            `json:"paddingLeft"`
	PaddingRight bool            `json:"paddingRight"`
}

// parseInlayHints 解码提示数组、单个提示或 null。畸形和 null 条目被跳过；缺失字段保持零值。
// 畸形和 null 条目被跳过；缺失字段保持零值。
func parseInlayHints(raw json.RawMessage) []InlayHint {
	entries := advancedRawEntries(raw)
	if len(entries) == 0 {
		return nil
	}
	out := make([]InlayHint, 0, len(entries))
	for _, entry := range entries {
		trimmed := bytes.TrimSpace(entry)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var hint rawInlayHint
		if err := json.Unmarshal(trimmed, &hint); err != nil {
			continue
		}
		item := InlayHint{
			Label:        renderMarkup(hint.Label),
			Kind:         hint.Kind,
			Tooltip:      renderMarkup(hint.Tooltip),
			PaddingLeft:  hint.PaddingLeft,
			PaddingRight: hint.PaddingRight,
		}
		if hint.Position != nil {
			item.Position = *hint.Position
		}
		out = append(out, item)
	}
	return out
}

// rawCodeLens 镜像线上形态。IsResolved 不被传输。
type rawCodeLens struct {
	Range   *Range   `json:"range"`
	Command *Command `json:"command"`
}

// parseCodeLens 解码 lens 数组、单个 lens 或 null。带 command 的 lens 已解析；不带 command 的
// 带 command 的 lens 已解析；不带 command 的
// 需要 codeLens/resolve。既无 range 又无 command 的条目被丢弃。
func parseCodeLens(raw json.RawMessage) []CodeLens {
	entries := advancedRawEntries(raw)
	if len(entries) == 0 {
		return nil
	}
	out := make([]CodeLens, 0, len(entries))
	for _, entry := range entries {
		trimmed := bytes.TrimSpace(entry)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var lens rawCodeLens
		if err := json.Unmarshal(trimmed, &lens); err != nil {
			continue
		}
		if lens.Range == nil && lens.Command == nil {
			continue
		}
		item := CodeLens{Command: lens.Command, IsResolved: lens.Command != nil}
		if lens.Range != nil {
			item.Range = *lens.Range
		}
		out = append(out, item)
	}
	return out
}
