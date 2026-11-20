package lsp

import (
	"bytes"
	"context"
	"encoding/json"
)

// DocumentSymbol returns the symbols declared in uri, flattened from the
// server's DocumentSymbol tree. Depth records nesting (top level is 0).
func (c *Client) DocumentSymbol(ctx context.Context, uri string) ([]Symbol, error) {
	raw, err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})
	if err != nil {
		return nil, err
	}
	return parseSymbols(raw), nil
}

// DocumentSymbolAt opens path (if needed) and lists its symbols.
func (c *Client) DocumentSymbolAt(ctx context.Context, path string) ([]Symbol, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.DocumentSymbol(ctx, uri)
}

// References returns every reference to the symbol at position.
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

// ReferencesAt opens path (if needed) and finds references at position.
func (c *Client) ReferencesAt(ctx context.Context, path string, position Position, includeDeclaration bool) ([]Location, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.References(ctx, uri, position, includeDeclaration)
}

// CodeActions returns the actions the server offers for a range. The supplied
// diagnostics are forwarded in the request context so quick fixes can target
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

// CodeActionsAt opens path (if needed) and offers actions for a range.
func (c *Client) CodeActionsAt(ctx context.Context, path string, rng Range, diagnostics []Diagnostic) ([]CodeAction, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.CodeActions(ctx, uri, rng, diagnostics)
}

// Rename asks the server for the edits that rename the symbol at position.
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

// RenameAt opens path (if needed) and computes rename edits there.
func (c *Client) RenameAt(ctx context.Context, path string, position Position, newName string) (*WorkspaceEdit, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Rename(ctx, uri, position, newName)
}

// Formatting returns the edits that format the whole document.
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

// FormattingAt opens path (if needed) and returns its formatting edits.
func (c *Client) FormattingAt(ctx context.Context, path string, options FormattingOptions) ([]TextEdit, error) {
	uri, err := c.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	return c.Formatting(ctx, uri, options)
}

// WorkspaceSymbols queries the workspace for symbols matching query.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]SymbolInformation, error) {
	raw, err := c.call(ctx, "workspace/symbol", map[string]any{"query": query})
	if err != nil {
		return nil, err
	}
	return parseSymbolInformation(raw), nil
}

// rawArray returns the elements of a JSON array result, or nil for null, an
// empty result, or a non-array value.
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

// rawDocumentSymbol covers both the tree-shaped DocumentSymbol and the flat
// SymbolInformation shape some servers return for documentSymbol.
type rawDocumentSymbol struct {
	Name           string            `json:"name"`
	Kind           int               `json:"kind"`
	Range          *Range            `json:"range"`
	SelectionRange *Range            `json:"selectionRange"`
	Detail         string            `json:"detail"`
	Children       []json.RawMessage `json:"children"`
	Location       *Location         `json:"location"`
}

// parseSymbols flattens a textDocument/documentSymbol result. DocumentSymbol
// children are walked recursively with an increasing Depth; a server that
// returns SymbolInformation entries is normalized from each location.
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
			// SymbolInformation fallback: a leaf with a location and no children.
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

// parseSymbolInformation decodes a workspace/symbol result.
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

// parseTextEdits decodes a formatting result: an array of TextEdits, a single
// TextEdit object, or null.
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

// parseWorkspaceEdit decodes a WorkspaceEdit, accepting both the legacy
// changes map and the documentChanges array. Text edits from both shapes are
// merged into Changes so callers can read a single per-file view.
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
			// Resource operation (create/rename/delete): no text edits to apply.
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

// rawCodeAction mirrors the wire shape of a CodeAction. Diagnostics stay raw
// because their severity is numeric on the wire but a word in Diagnostic.
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

// parseCodeActions decodes a textDocument/codeAction result. Command-shaped
// legacy entries are kept via their Command field.
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

// lspDiagnostics converts our word-severity diagnostics back to the numeric
// form that the code action request context expects.
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
