package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Line-level recovery for spill artifacts. This is a PURE helper: it writes
// nothing, spools nothing and does not consult the tool registry. It exists so
// a model (or a future wiring point) can continue reading a spilled, truncated
// result by line range instead of re-reading the whole artifact.
//
// The line numbering and trailing-newline accounting deliberately mirror the
// read tool's offset/limit path (see tools_builtin_read.go): "1-based first
// line", an unbounded end when endLine <= 0, and a trailing newline that is a
// terminator rather than a phantom extra line.

var (
	// errSpillInvalidPath marks a path that is not a usable spill artifact
	// location (empty, traversing, a directory or a symlink).
	errSpillInvalidPath = errors.New("tools: invalid spill artifact path")
	// errSpillNotArtifact marks a path that does not carry the canonical
	// artifact filename this package writes.
	errSpillNotArtifact = errors.New("tools: not a spill artifact")
)

// ReadSpillRange returns the inclusive 1-based line range [startLine, endLine]
// from a spill artifact, together with the artifact's total line count so the
// caller can decide whether to page further.
//
// Semantics (matching the read tool):
//   - startLine < 1 is clamped to 1; a startLine past the last real line
//     returns empty content (not an error);
//   - endLine <= 0 means "to end of file"; a positive endLine past the last
//     line is clamped to it;
//   - a bounded read has no trailing newline, while a to-EOF read preserves
//     the file's original trailing newline (so a full read is byte-identical);
//   - startLine > endLine is an empty range.
//
// The path is validated as a spill artifact before any I/O: it must have the
// canonical artifact filename, contain no ".." traversal segment and resolve
// to a regular, non-symlink file. Together these keep arbitrary files (session
// secrets, config, /etc/passwd, ...) unreadable through this helper.
func ReadSpillRange(path string, startLine, endLine int) (content string, totalLines int, err error) {
	// Deriving the validation root from the artifact's own directory means the
	// artifact must be a direct, flat child of its spill directory -- the exact
	// layout writeSpillArtifact produces (filepath.Join(dir, name)).
	return readSpillRangeInDir(filepath.Dir(path), path, startLine, endLine)
}

// readSpillRangeInDir is the dir-aware core. It reuses spillArtifactAbsInDir --
// the same path validation isOwnSpillResult uses to recognise our artifacts --
// so a path escape or a look-alike filename can never reach the file read.
// Keeping the dir explicit makes this the natural wiring point once a caller
// has a configured spill directory to pass in.
func readSpillRangeInDir(dir, path string, startLine, endLine int) (content string, totalLines int, err error) {
	if strings.TrimSpace(path) == "" {
		return "", 0, fmt.Errorf("%w: empty path", errSpillInvalidPath)
	}
	if e := rejectSpillTraversal(path); e != nil {
		return "", 0, e
	}

	abs, ok := spillArtifactAbsInDir(dir, path)
	if !ok {
		return "", 0, fmt.Errorf("%w: %q", errSpillNotArtifact, path)
	}
	// spillArtifactAbsInDir uses os.Stat (which follows symlinks). Spill files
	// are always created as regular files with O_EXCL, so reject a symlink that
	// could otherwise point at an arbitrary target outside the spill dir.
	info, err := os.Lstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", 0, fmt.Errorf("%w: %q is not a regular spill file", errSpillInvalidPath, path)
	}

	return readSpillRangeAt(abs, startLine, endLine)
}

// rejectSpillTraversal rejects ".." path segments before filepath.Abs and
// filepath.Clean can hide them. Without this, "../../secret/tool-output-<id>.log"
// would be cleaned into a path outside the spill directory.
func rejectSpillTraversal(path string) error {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == ".." {
			return fmt.Errorf("%w: traversal segment in %q", errSpillInvalidPath, path)
		}
	}
	return nil
}

// readSpillRangeAt performs the actual bounded read on an already-validated
// absolute artifact path. Line splitting is rune-safe because '\n' (0x0A) can
// never occur inside a multi-byte UTF-8 rune; utf8.Valid rejects any artifact
// that somehow carries corrupt bytes.
func readSpillRangeAt(abs string, startLine, endLine int) (content string, totalLines int, err error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", 0, fmt.Errorf("read spill artifact: %w", err)
	}
	if !utf8.Valid(data) {
		return "", 0, fmt.Errorf("spill artifact %q is not UTF-8 text", abs)
	}

	lines, total := splitSpillLines(data)
	if total == 0 { // empty artifact
		return "", 0, nil
	}

	start := startLine
	if start < 1 {
		start = 1
	}
	if start > total {
		return "", total, nil
	}

	var end int
	if endLine <= 0 {
		// Read to EOF, including the trailing sentinel, so a full read is
		// byte-identical to the original artifact.
		end = len(lines)
	} else {
		end = endLine
		if end > total {
			end = total
		}
	}
	if end < start {
		return "", total, nil
	}

	return strings.Join(lines[start-1:end], "\n"), total, nil
}

// splitSpillLines mirrors the read tool's accounting: strings.Split keeps an
// empty sentinel after a final newline, which terminates the last line rather
// than starting a new, real one. Kept in the spill helper (instead of importing
// the read tool's internals) so the packages stay decoupled.
func splitSpillLines(data []byte) (lines []string, totalLines int) {
	if len(data) == 0 {
		return nil, 0
	}
	lines = strings.Split(string(data), "\n")
	totalLines = len(lines)
	if data[len(data)-1] == '\n' {
		totalLines--
	}
	return lines, totalLines
}
