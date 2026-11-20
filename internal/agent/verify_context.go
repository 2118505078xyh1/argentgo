package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	maxSourceAnchors   = 8
	maxSourceFileBytes = 1 << 20 // 1 MiB per file
	maxSourceLineBytes = 240     // per emitted source line
	sourceTruncMark    = "..."

	// sourceAnchorPattern matches file:line references in tool output.
	// Group 1 is an optional Windows drive prefix ("C:\"), group 2 the path,
	// group 3 the line number and group 4 an optional column number.
	sourceAnchorPattern = `([A-Za-z]:[\\/])?([A-Za-z0-9._\-/\\]+):([0-9]{1,6})(?::([0-9]{1,6}))?`
)

type sourceAnchor struct {
	name string // path as it appeared in the tool output
	path string // resolved, root-confined absolute path
	line int
}

// AnchorSourceLines turns file:line references in output into short source
// windows with a █ marker on the offending line. It composes with (and does
// not replace) VerificationFeedback: callers append the returned block to the
// feedback text so a lint/test failure shows the offending source.
//
// The function never writes to disk and never escapes root: absolute paths
// outside root and any path containing a .. escape are ignored. File reads are
// bounded (1 MiB per file, at most maxSourceAnchors anchors), binary/NUL
// files are skipped, and every emitted line is clipped on a UTF-8 rune
// boundary and redacted through llm.RedactText. Output is a sequence of
// blocks of the form:
//
//	fname:12:
//	10| line before
//	11| line before
//	█12| offending line
//	13| line after
//
// If no usable anchor is found, or every referenced file is unreadable, it
// returns "" so callers can leave their feedback unchanged.
func AnchorSourceLines(output, root string, pad int) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	if pad < 0 {
		pad = 0
	}

	re := regexp.MustCompile(sourceAnchorPattern)
	seen := make(map[string]bool)
	anchors := make([]sourceAnchor, 0, maxSourceAnchors)
	for _, m := range re.FindAllStringSubmatch(output, -1) {
		name := m[1] + m[2]
		if !looksLikeSourcePath(name) {
			continue
		}
		line, err := strconv.Atoi(m[3])
		if err != nil || line < 1 {
			continue
		}
		path, ok := resolveSourcePath(name, rootAbs)
		if !ok {
			continue
		}
		key := path + "\x00" + strconv.Itoa(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		anchors = append(anchors, sourceAnchor{name: name, path: path, line: line})
		if len(anchors) == maxSourceAnchors {
			break
		}
	}
	if len(anchors) == 0 {
		return ""
	}

	var b strings.Builder
	for _, a := range anchors {
		if renderSourceWindow(&b, a, pad) {
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// looksLikeSourcePath filters out non-path tokens such as "ERROR:404". A real
// file reference always carries a separator or a file extension.
func looksLikeSourcePath(s string) bool {
	return strings.ContainsAny(s, "./\\")
}

// resolveSourcePath confines raw to root. Relative paths are joined onto root
// and re-checked with filepath.Rel; absolute paths are accepted only when
// they are already inside root. Any .. escape or outside-root result is
// rejected. The returned path is absolute and always underneath root.
func resolveSourcePath(raw, root string) (string, bool) {
	cleaned := filepath.Clean(raw)
	if cleaned == "" || cleaned == "." {
		return "", false
	}
	if filepath.IsAbs(cleaned) {
		rel, err := filepath.Rel(root, cleaned)
		if err != nil || isOutsideRoot(rel) {
			return "", false
		}
		return filepath.Join(root, rel), true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	joined := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, joined)
	if err != nil || isOutsideRoot(rel) {
		return "", false
	}
	return joined, true
}

func isOutsideRoot(rel string) bool {
	return rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel)
}

// renderSourceWindow appends one anchored window to b. It reports whether a
// window was emitted (false when the file is unreadable or the line is past
// the end of the file).
func renderSourceWindow(b *strings.Builder, a sourceAnchor, pad int) bool {
	lines, ok := readSourceLines(a.path)
	if !ok || a.line > len(lines) {
		return false
	}
	start := a.line - pad
	if start < 1 {
		start = 1
	}
	end := a.line + pad
	if end > len(lines) {
		end = len(lines)
	}
	width := len(strconv.Itoa(end))

	b.WriteString(llm.RedactText(a.name))
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(a.line))
	b.WriteString(":\n")
	for n := start; n <= end; n++ {
		marker := " "
		if n == a.line {
			marker = "█"
		}
		fmt.Fprintf(b, "%s%*d| %s\n", marker, width, n, clipSourceLine(lines[n-1]))
	}
	return true
}

// readSourceLines reads a bounded, text-only source file. NUL bytes, invalid
// UTF-8, empty files and files larger than maxSourceFileBytes are skipped.
func readSourceLines(path string) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxSourceFileBytes+1))
	if err != nil || len(data) > maxSourceFileBytes || len(data) == 0 {
		return nil, false
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, false
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	// A trailing newline terminates the last real line; it is not an extra
	// empty line to show in a window.
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, true
}

// clipSourceLine redacts then bounds one source line, cutting on a rune
// boundary so the emitted text is always valid UTF-8.
func clipSourceLine(line string) string {
	line = llm.RedactText(line)
	if len(line) <= maxSourceLineBytes {
		return line
	}
	cut := maxSourceLineBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + sourceTruncMark
}
