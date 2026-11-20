package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
)

// Definition 返回 position 处符号的声明位置。
func (c *Client) Definition(ctx context.Context, uri string, position Position) ([]Location, error) {
	raw, err := c.call(ctx, "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(raw), nil
}

// DefinitionAt 打开 path（若需要）并在那里请求定义。
func (c *Client) DefinitionAt(ctx context.Context, path string, position Position) ([]Location, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Definition(ctx, uri, position)
}

// Hover 返回 position 处的文档，服务器没有时为 nil。
func (c *Client) Hover(ctx context.Context, uri string, position Position) (*Hover, error) {
	raw, err := c.call(ctx, "textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     position,
	})
	if err != nil {
		return nil, err
	}
	return parseHover(raw), nil
}

// HoverAt 打开 path（若需要）并在那里请求 hover 信息。
func (c *Client) HoverAt(ctx context.Context, path string, position Position) (*Hover, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Hover(ctx, uri, position)
}

// rawLocation 同时覆盖 Location 和 LocationLink。
type rawLocation struct {
	URI                  string `json:"uri"`
	Range                *Range `json:"range"`
	TargetURI            string `json:"targetUri"`
	TargetSelectionRange *Range `json:"targetSelectionRange"`
	TargetRange          *Range `json:"targetRange"`
}

// parseLocations 把 null、单个 Location、Location 数组和 LocationLink 数组规范化为一个切片。
// 和 LocationLink 数组规范化为一个切片。
func parseLocations(raw json.RawMessage) []Location {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	var entries []rawLocation
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &entries); err != nil {
			return nil
		}
	} else {
		var single rawLocation
		if err := json.Unmarshal(trimmed, &single); err != nil {
			return nil
		}
		entries = []rawLocation{single}
	}

	out := make([]Location, 0, len(entries))
	for _, entry := range entries {
		uri := entry.URI
		span := entry.Range
		if entry.TargetURI != "" {
			uri = entry.TargetURI
			switch {
			case entry.TargetSelectionRange != nil:
				span = entry.TargetSelectionRange
			case entry.TargetRange != nil:
				span = entry.TargetRange
			}
		}
		if uri == "" {
			continue
		}
		location := Location{URI: uri}
		if span != nil {
			location.Range = *span
		}
		out = append(out, location)
	}
	return out
}

type rawHover struct {
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range"`
}

func parseHover(raw json.RawMessage) *Hover {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	var payload rawHover
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil
	}
	text := renderMarkup(payload.Contents)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return &Hover{Contents: text, Range: payload.Range}
}

// renderMarkup 展平 MarkupContent、MarkedString 以及它们的数组。
func renderMarkup(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		return text
	}
	var marked struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(trimmed, &marked); err == nil {
		return marked.Value
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return ""
	}
	rendered := make([]string, 0, len(items))
	for _, item := range items {
		if part := strings.TrimSpace(renderMarkup(item)); part != "" {
			rendered = append(rendered, part)
		}
	}
	return strings.Join(rendered, "\n\n")
}
