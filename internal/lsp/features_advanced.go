package lsp

import (
	"bytes"
	"context"
	"encoding/json"
)

// SemanticTokens asks the server for the full semantic token stream of uri.
// A null result is not an error: it yields nil tokens.
func (c *Client) SemanticTokens(ctx context.Context, uri string) (*SemanticTokens, error) {
	raw, err := c.call(ctx, "textDocument/semanticTokens/full", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	if err != nil {
		return nil, err
	}
	return parseSemanticTokens(raw), nil
}

// SemanticTokensAt opens path (if needed) and returns its semantic tokens.
func (c *Client) SemanticTokensAt(ctx context.Context, path string) (*SemanticTokens, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.SemanticTokens(ctx, uri)
}

// InlayHints returns the inline annotations the server offers within rng.
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

// InlayHintsAt opens path (if needed) and lists its inlay hints in rng.
func (c *Client) InlayHintsAt(ctx context.Context, path string, rng Range) ([]InlayHint, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.InlayHints(ctx, uri, rng)
}

// CodeLens lists the clickable labels the server attaches to uri.
func (c *Client) CodeLens(ctx context.Context, uri string) ([]CodeLens, error) {
	raw, err := c.call(ctx, "textDocument/codeLens", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	if err != nil {
		return nil, err
	}
	return parseCodeLens(raw), nil
}

// CodeLensAt opens path (if needed) and lists its code lenses.
func (c *Client) CodeLensAt(ctx context.Context, path string) ([]CodeLens, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.CodeLens(ctx, uri)
}

// ResolveCodeLens fills in the command of a lens that lacked one. The protocol
// takes the lens itself as params; the returned copy is always marked resolved.
// Opaque server "data" is not retained by CodeLens, so servers that require it
// to resolve a lens cannot be served through this helper.
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

// advancedRawEntries returns the elements of a JSON array result, or a
// one-element slice for a single object result. Null, empty, malformed and
// scalar results yield nil. It lets the parsers below accept lenient servers
// without changing the shared helpers in features_ide.go.
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
		// Validate before returning: a lone malformed object must not be
		// mistaken for a single (empty) result.
		var object map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &object); err != nil {
			return nil
		}
		return []json.RawMessage{trimmed}
	default:
		return nil
	}
}

// parseSemanticTokens decodes a textDocument/semanticTokens/full result. The
// result is one SemanticTokens object; if a server wraps it in an array, the
// first object is used. Data is kept verbatim: the five-int-per-token stream
// is delta-encoded and only the UI knows how to turn it back into lines.
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

// rawInlayHint mirrors the wire shape. Label and tooltip are kept raw because
// each may be a string, a MarkupContent object or an array of parts.
type rawInlayHint struct {
	Position     *Position       `json:"position"`
	Label        json.RawMessage `json:"label"`
	Kind         int             `json:"kind"`
	Tooltip      json.RawMessage `json:"tooltip"`
	PaddingLeft  bool            `json:"paddingLeft"`
	PaddingRight bool            `json:"paddingRight"`
}

// parseInlayHints decodes an array of hints, a single hint, or null. Malformed
// and null entries are skipped; missing fields keep their zero values.
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

// rawCodeLens mirrors the wire shape. IsResolved is not transmitted.
type rawCodeLens struct {
	Range   *Range   `json:"range"`
	Command *Command `json:"command"`
}

// parseCodeLens decodes an array of lenses, a single lens, or null. A lens
// carrying a command is already resolved; one without a command needs
// codeLens/resolve. Entries with neither a range nor a command are dropped.
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
