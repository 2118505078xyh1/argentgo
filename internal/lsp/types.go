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
