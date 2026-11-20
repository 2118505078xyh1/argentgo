package lsp

import "fmt"

// Position is a zero-based location in a document, as LSP defines it.
type Position struct {
	// Line is zero-based.
	Line int `json:"line"`
	// Character is a zero-based UTF-16 code unit offset within the line.
	Character int `json:"character"`
}

// String renders the position as 1-based line:column, the way humans count.
func (p Position) String() string {
	return fmt.Sprintf("%d:%d", p.Line+1, p.Character+1)
}

// Human converts a 1-based line and column to a Position.
func Human(line, column int) Position {
	if line < 1 {
		line = 1
	}
	if column < 1 {
		column = 1
	}
	return Position{Line: line - 1, Character: column - 1}
}

// Range is a span between two positions.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic is one problem the server reported for a document.
type Diagnostic struct {
	URI      string `json:"uri"`
	Range    Range  `json:"range"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

// Location is a definition target, normalized from Location or LocationLink.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Hover is the documentation the server offered at a position.
type Hover struct {
	Contents string
	Range    *Range
}

// TextEdit is one replacement of a document range with new text.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// Symbol is one flat entry from a document symbol tree or a workspace symbol
// list. Depth is 0 for top-level entries and grows with nesting; it is derived
// by the client, so it is not part of the wire format.
type Symbol struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
	URI            string `json:"uri,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Depth          int    `json:"-"`
}

// SymbolInformation is the flat SymbolInformation shape returned by
// workspace/symbol and by servers that use it for document symbols.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Location      Location `json:"location"`
	ContainerName string   `json:"containerName,omitempty"`
	Deprecated    bool     `json:"deprecated,omitempty"`
	Tags          []int    `json:"tags,omitempty"`
}

// DocumentEdit is one file's text edits from a WorkspaceEdit.documentChanges
// array. Resource operations (create/rename/delete) carry no text edits and
// are skipped.
type DocumentEdit struct {
	URI   string     `json:"uri"`
	Edits []TextEdit `json:"edits"`
}

// WorkspaceEdit groups text edits by file. Changes always holds the merged
// view of the legacy changes map and documentChanges; DocumentChanges keeps
// the per-file entries as the server sent them.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []DocumentEdit        `json:"documentChanges,omitempty"`
}

// Command describes a command reference carried by a code action.
type Command struct {
	Title     string `json:"title"`
	Command   string `json:"command"`
	Arguments []any  `json:"arguments,omitempty"`
}

// CodeActionDisabled explains why an action is unavailable.
type CodeActionDisabled struct {
	Reason string `json:"reason"`
}

// CodeAction is one fix or refactor the server offers for a range.
type CodeAction struct {
	Title       string              `json:"title"`
	Kind        string              `json:"kind,omitempty"`
	Diagnostics []Diagnostic        `json:"diagnostics,omitempty"`
	Edit        *WorkspaceEdit      `json:"edit,omitempty"`
	Command     *Command            `json:"command,omitempty"`
	IsPreferred bool                `json:"isPreferred,omitempty"`
	Disabled    *CodeActionDisabled `json:"disabled,omitempty"`
}

// FormattingOptions is the standard formatting options object. Pointer
// booleans distinguish "unset" from an explicit false.
type FormattingOptions struct {
	TabSize                int   `json:"tabSize"`
	InsertSpaces           bool  `json:"insertSpaces"`
	TrimTrailingWhitespace *bool `json:"trimTrailingWhitespace,omitempty"`
	InsertFinalNewline     *bool `json:"insertFinalNewline,omitempty"`
	TrimFinalNewlines      *bool `json:"trimFinalNewlines,omitempty"`
}

// SemanticTokens is the result of a textDocument/semanticTokens/full request.
type SemanticTokens struct {
	// Data is the flat, delta-encoded token stream: five uint32 values per
	// token (deltaLine, deltaStart, length, tokenType, tokenModifiers). The
	// client keeps the raw slice; decoding it into lines is a UI concern.
	Data []uint32 `json:"data"`
	// ResultID is the server's version tag, used by semanticTokens/full/delta.
	ResultID string `json:"resultId"`
}

// InlayHint is one inline annotation the server offers for a document.
type InlayHint struct {
	Position Position `json:"position"`
	// Label is the text shown to the user. Label parts from the wire are
	// flattened to plain text by the parser.
	Label        string `json:"label"`
	Kind         int    `json:"kind,omitempty"`
	Tooltip      string `json:"tooltip,omitempty"`
	PaddingLeft  bool   `json:"paddingLeft,omitempty"`
	PaddingRight bool   `json:"paddingRight,omitempty"`
}

// CodeLens is one clickable label attached to a range. IsResolved is derived
// by the client: a lens without a command still needs codeLens/resolve.
type CodeLens struct {
	Range      Range    `json:"range"`
	Command    *Command `json:"command,omitempty"`
	IsResolved bool     `json:"-"`
}

// severityName maps the numeric LSP severity to a word.
func severityName(severity int) string {
	switch severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "information"
	case 4:
		return "hint"
	default:
		return "error"
	}
}
