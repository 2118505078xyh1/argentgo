package tools

// LSP IDE capability tools. These expose the language-server methods that were
// added for IDE support (document/workspace symbols, references, code actions,
// rename and formatting) to the model as read-only tools.
//
// DEPENDENCY CONTRACT
// -------------------
// internal/tools must not import internal/lsp (the archguard test enforces the
// core layering), and must not import its types either. Instead this file owns
// small neutral DTOs that mirror the JSON shape of the corresponding lsp types,
// plus a consumer-side interface (LspIde) whose methods return those DTOs.
//
// WIRING: internal/app must inject an adapter that wraps *lsp.Client, calls the
// typed IDE methods, and converts their results into the neutral DTOs below.
// The adapter is installed on the Registry with SetLspIde. Until it is
// installed the six tools are still advertised in Specs but report
// "LSP IDE integration is not available" without an error.
//
// The adapter has this exact shape (lsp types are converted field-by-field):
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
// All six tools are READ-ONLY previews: rename and formatting return the edits
// the server proposes as text and never apply them. The actual modification is
// performed by the ordinary edit/write tools. Consequently Mutating() is false
// for every tool here and they remain available in the read-only sandbox.

import (
	"context"
	"encoding/json"
	"fmt"
)

// lspIdeUnavailable is returned (without an error) when no adapter has been
// wired yet. Keeping it a graceful message lets the model see the capability
// exists without failing the turn in environments with no language server.
const lspIdeUnavailable = "LSP IDE integration is not available"

// ---- Neutral DTOs mirroring the JSON shape of the lsp types ----

// LspPos is a zero-based LSP position (line and UTF-16 character offset).
type LspPos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LspRange is a span between two zero-based positions.
type LspRange struct {
	Start LspPos `json:"start"`
	End   LspPos `json:"end"`
}

// LspLocation is a definition/reference target.
type LspLocation struct {
	URI   string   `json:"uri"`
	Range LspRange `json:"range"`
}

// LspSymbol is one flat document-symbol entry. Kind mirrors the numeric LSP
// symbol kind; Depth is derived by the client and is not part of the wire JSON.
type LspSymbol struct {
	Name           string   `json:"name"`
	Kind           int      `json:"kind"`
	Range          LspRange `json:"range"`
	SelectionRange LspRange `json:"selectionRange"`
	URI            string   `json:"uri,omitempty"`
	Detail         string   `json:"detail,omitempty"`
	Depth          int      `json:"-"`
}

// LspTextEdit is one replacement of a document range with new text.
type LspTextEdit struct {
	Range   LspRange `json:"range"`
	NewText string   `json:"newText"`
}

// LspDocumentEdit is one file's edits from a WorkspaceEdit.documentChanges.
type LspDocumentEdit struct {
	URI   string        `json:"uri"`
	Edits []LspTextEdit `json:"edits"`
}

// LspWorkspaceEdit groups text edits by file URI.
type LspWorkspaceEdit struct {
	Changes         map[string][]LspTextEdit `json:"changes,omitempty"`
	DocumentChanges []LspDocumentEdit        `json:"documentChanges,omitempty"`
}

// LspDiagnostic is one problem attached to a code action.
type LspDiagnostic struct {
	Range    LspRange `json:"range"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Source   string   `json:"source,omitempty"`
}

// LspCommand is a command reference carried by a code action. Arguments stay
// as `any` because their shape is server-defined; this matches lsp.Command so
// the app adapter can assign the slice directly.
type LspCommand struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// LspCodeActionDisabled explains why an action is unavailable.
type LspCodeActionDisabled struct {
	Reason string `json:"reason"`
}

// LspCodeAction is one fix or refactor the server offers for a range.
type LspCodeAction struct {
	Title       string                 `json:"title"`
	Kind        string                 `json:"kind,omitempty"`
	Diagnostics []LspDiagnostic        `json:"diagnostics,omitempty"`
	Edit        *LspWorkspaceEdit      `json:"edit,omitempty"`
	Command     *LspCommand            `json:"command,omitempty"`
	IsPreferred bool                   `json:"isPreferred,omitempty"`
	Disabled    *LspCodeActionDisabled `json:"disabled,omitempty"`
}

// LspSymbolInfo is one flat entry from a workspace symbol query.
type LspSymbolInfo struct {
	Name          string      `json:"name"`
	Kind          int         `json:"kind"`
	Location      LspLocation `json:"location"`
	ContainerName string      `json:"containerName,omitempty"`
}

// LspIde is the consumer-side interface satisfied by the app-provided adapter
// around *lsp.Client. All positions are zero-based LSP coordinates; the tools
// pass them through unchanged.
type LspIde interface {
	DocumentSymbol(path string) ([]LspSymbol, error)
	References(path string, line, character int, includeDecl bool) ([]LspLocation, error)
	CodeActions(path string, startLine, startChar, endLine, endChar int) ([]LspCodeAction, error)
	Rename(path string, line, char int, newName string) (*LspWorkspaceEdit, error)
	Formatting(path string, tabSize int, insertSpaces bool) ([]LspTextEdit, error)
	WorkspaceSymbols(query string) ([]LspSymbolInfo, error)
}

// lspIdeTool is a single generic read-only tool. Each instance carries its own
// adapter and a call closure that decodes arguments and invokes one LspIde
// method, keeping the six tools DRY while preserving per-tool metadata.
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

// ---- Argument decoders ----

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

// ---- Constructors ----

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
