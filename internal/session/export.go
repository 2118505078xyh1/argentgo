package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// ExportOptions controls how ExportProject renders a transcript.
type ExportOptions struct {
	Branch   string // record id (or unique prefix) whose path to export; empty follows the active leaf
	Sanitize bool   // redact content, paths, tool payloads and credentials before rendering
	Format   string // "html" (default), "json" or "markdown"
}

// maxExportBytes bounds the size of a share file so a very large session cannot
// produce an unbounded artifact. Exceeding it appends exportTruncatedMarker.
const maxExportBytes = 2 << 20 // 2 MiB

const exportTruncatedMarker = "\n[truncated: export exceeded 2MiB limit]\n"

// sensitiveExportKeys maps a JSON key to the "kind" placed in its redaction
// placeholder. Fields with these names carry content, filesystem paths,
// commands or tool payloads and must never leave the process in clear text in
// a portable share file. The map is intentionally exhaustive: an unknown key
// still gets the credential-pattern pass from security.Redact.
var sensitiveExportKeys = map[string]string{
	"content":     "content",
	"text":        "text",
	"prompt":      "prompt",
	"message":     "message",
	"question":    "text",
	"reply":       "text",
	"note":        "text",
	"output":      "tool-output",
	"result":      "tool-output",
	"stdout":      "tool-output",
	"stderr":      "tool-output",
	"response":    "tool-output",
	"command":     "command",
	"cmd":         "command",
	"args":        "tool-args",
	"arguments":   "tool-args",
	"input":       "tool-args",
	"payload":     "tool-args",
	"path":        "path",
	"file":        "path",
	"filepath":    "path",
	"filename":    "path",
	"dir":         "path",
	"directory":   "path",
	"target":      "path",
	"cwd":         "path",
	"url":         "url",
	"uri":         "url",
	"title":       "title",
	"description": "description",
	"summary":     "summary",
	"reason":      "reason",
	"snippet":     "snippet",
	"skill":       "skill",
	"agent":       "agent",
	"shell":       "shell",
	"script":      "script",
	"code":        "code",
	"patch":       "patch",
	"diff":        "diff",
	"query":       "query",
}

// exportEntry is one record of a structured (JSON/Markdown) transcript.
type exportEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Tool string `json:"tool"`
	TS   string `json:"ts"`
	Kind string `json:"kind"`
}

// ExportProject renders one branch of a session as a portable, optionally
// redacted share file. project is either a *Session or a session directory
// (string); id identifies the session when a directory is given. branch in the
// argument list takes precedence over opts.Branch.
func ExportProject(project any, id, branch string, opts ExportOptions) ([]byte, error) {
	if branch == "" {
		branch = opts.Branch
	}
	format := strings.ToLower(strings.TrimSpace(opts.Format))
	if format == "" {
		format = "html"
	}
	switch format {
	case "html", "json", "markdown":
	default:
		return nil, fmt.Errorf("session: unsupported export format %q (want html, json or markdown)", opts.Format)
	}

	records, path, sessionID, err := loadExportRecords(project, id)
	if err != nil {
		return nil, err
	}
	if id != "" {
		sessionID = id
	}
	if sessionID == "" {
		sessionID = "session"
	}

	chain, err := exportBranch(records, branch)
	if err != nil {
		return nil, err
	}
	if opts.Sanitize {
		chain = sanitizeRecords(chain)
	}
	if len(chain) == 0 {
		return capExport(emptyExport(format, sessionID)), nil
	}

	var out []byte
	switch format {
	case "html":
		out, err = renderExportHTML(path, chain, opts.Sanitize)
	case "json":
		out, err = renderExportJSON(chain)
	case "markdown":
		out, err = renderExportMarkdown(sessionID, chain)
	}
	if err != nil {
		return nil, err
	}
	return capExport(out), nil
}

// loadExportRecords resolves project into records, its backing path and id.
func loadExportRecords(project any, id string) ([]Record, string, string, error) {
	switch p := project.(type) {
	case *Session:
		if p == nil {
			return nil, "", "", fmt.Errorf("session: nil session passed to export")
		}
		records, err := p.Records()
		return records, p.path(), p.ID(), err
	case string:
		path, err := ResolvePath(p, id)
		if err != nil {
			return nil, "", id, err
		}
		records, err := readRecords(path)
		return records, path, id, err
	default:
		return nil, "", id, fmt.Errorf("session: unsupported export project type %T", project)
	}
}

// exportBranch selects the root-to-leaf record chain for branch. It mirrors
// RenderHTML's walk: an empty branch follows the active (last) record, and an
// unknown branch is an error rather than a silent fallback.
func exportBranch(records []Record, branch string) ([]Record, error) {
	if len(records) == 0 {
		return nil, nil
	}
	byID := indexByID(records)
	leaf := branch
	if leaf != "" {
		// Accept a full record id or a unique prefix, mirroring ResolvePath.
		if _, ok := byID[leaf]; !ok {
			if resolved, ok := resolveRecordPrefix(byID, leaf); ok {
				leaf = resolved
			}
		}
	}
	if leaf == "" {
		leaf = records[len(records)-1].ID
	}
	if _, ok := byID[leaf]; !ok {
		return nil, fmt.Errorf("session: unknown branch %q", branch)
	}
	chain := make([]Record, 0, len(records))
	seen := make(map[string]bool, len(records))
	for current := leaf; current != ""; {
		record, ok := byID[current]
		if !ok || seen[current] {
			break
		}
		seen[current] = true
		chain = append(chain, record)
		current = record.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
}

func resolveRecordPrefix(byID map[string]Record, prefix string) (string, bool) {
	found := ""
	for id := range byID {
		if strings.HasPrefix(id, prefix) {
			if found != "" {
				return "", false // ambiguous
			}
			found = id
		}
	}
	return found, found != ""
}

// sanitizeRecords returns a deep copy of records whose Data payloads have all
// sensitive fields replaced with "[redacted:<kind>:<id>]" placeholders and
// whose remaining strings have passed through security.Redact.
func sanitizeRecords(records []Record) []Record {
	out := make([]Record, len(records))
	for i, record := range records {
		out[i] = sanitizeRecord(record)
	}
	return out
}

func sanitizeRecord(record Record) Record {
	if len(record.Data) == 0 || string(record.Data) == "null" {
		return record
	}
	var value any
	// Decode with UseNumber so integer payloads survive the round trip exactly.
	// json.Unmarshal into any would turn every number into float64, silently
	// rounding values above 2^53 (large ids, timestamps in microseconds, token
	// counters) and corrupting an otherwise faithful sanitized export.
	dec := json.NewDecoder(bytes.NewReader(record.Data))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		record.Data = redactedJSON("data", record.ID)
		return record
	}
	var sanitized any
	if _, ok := value.(string); ok {
		// A bare string payload is message text with no key to classify it.
		sanitized = redactedText("text", record.ID)
	} else {
		sanitized = sanitizeExportValue(value, "", record.ID)
	}
	data, err := json.Marshal(sanitized)
	if err != nil {
		record.Data = redactedJSON("data", record.ID)
		return record
	}
	record.Data = data
	return record
}

func sanitizeExportValue(value any, key string, id string) any {
	switch typed := value.(type) {
	case string:
		if kind, ok := sensitiveExportKeys[strings.ToLower(key)]; ok {
			return redactedText(kind, id)
		}
		return security.Redact(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, inner := range typed {
			out[k] = sanitizeExportValue(inner, k, id)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, inner := range typed {
			// Array elements inherit the parent key: `args: ["cmd", ...]`
			// must redact every element, not just the array itself.
			out[i] = sanitizeExportValue(inner, key, id)
		}
		return out
	default:
		// Numbers, booleans and null carry no free-form text.
		return value
	}
}

func redactedText(kind, id string) string {
	return fmt.Sprintf("[redacted:%s:%s]", kind, id)
}

func redactedJSON(kind, id string) json.RawMessage {
	data, _ := json.Marshal(redactedText(kind, id))
	return data
}

// renderExportHTML delegates to RenderHTML. HTML cannot be built from records
// in memory without editing tree.go, so a sanitized export writes the already
// redacted chain to a temporary JSONL file and renders that instead. For an
// unsanitized export the original file is rendered directly, but the leaf of
// the already-resolved chain must still be passed as the branch: RenderHTML
// with an empty branch follows the active (last) record, which would silently
// export the wrong path when the caller selected a non-active branch.
func renderExportHTML(path string, chain []Record, sanitize bool) ([]byte, error) {
	if !sanitize {
		leaf := ""
		if len(chain) > 0 {
			leaf = chain[len(chain)-1].ID
		}
		var buf bytes.Buffer
		if err := RenderHTML(path, leaf, &buf); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	file, err := os.CreateTemp("", "argent-export-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("session: create export temp file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	for _, record := range chain {
		line, err := json.Marshal(record)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("session: encode export record: %w", err)
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			file.Close()
			return nil, fmt.Errorf("session: write export temp file: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("session: close export temp file: %w", err)
	}
	var buf bytes.Buffer
	if err := RenderHTML(tempPath, "", &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderExportJSON(chain []Record) ([]byte, error) {
	entries := make([]exportEntry, 0, len(chain))
	for _, record := range chain {
		entries = append(entries, exportEntryFromRecord(record))
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("session: encode json export: %w", err)
	}
	return append(data, '\n'), nil
}

func renderExportMarkdown(id string, chain []Record) ([]byte, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "# Session %s\n\n", id)
	for _, record := range chain {
		entry := exportEntryFromRecord(record)
		fmt.Fprintf(&buf, "**%s**: %s\n\n", entry.Role, entry.Text)
	}
	return buf.Bytes(), nil
}

// exportEntryFromRecord projects one record onto the export schema. Metadata
// records (header, attempt, compaction, ...) are rendered with a best-effort
// text so the share file is a complete transcript rather than messages only.
func exportEntryFromRecord(record Record) exportEntry {
	entry := exportEntry{Kind: record.Kind, Role: record.Kind}
	if !record.TS.IsZero() {
		entry.TS = record.TS.UTC().Format(time.RFC3339)
	}
	if message, ok := messageEntry(record); ok {
		entry.Role = message.Role
		entry.Text = message.Content
		entry.Tool = message.Name
		return entry
	}
	switch record.Kind {
	case KindHeader:
		entry.Role = "system"
		entry.Text = fieldString(record.Data, "title")
	case KindAttempt:
		entry.Role = "system"
		var attempt Attempt
		if jsonUnmarshal(record.Data, &attempt) == nil {
			entry.Text = strings.TrimSpace(attempt.Reason + " " + attempt.Snippet())
		}
		if entry.Text == "" {
			entry.Text = redactedText("attempt", record.ID)
		}
	case KindCompaction:
		entry.Role = "system"
		var compaction Compaction
		if json.Unmarshal(record.Data, &compaction) == nil {
			entry.Text = compaction.Summary
		}
	default:
		entry.Text = textOf(record.Data)
	}
	return entry
}

func emptyExport(format, id string) []byte {
	switch format {
	case "json":
		return []byte("[]\n")
	case "markdown":
		return []byte(fmt.Sprintf("# Session %s\n\n", id))
	default:
		return []byte(fmt.Sprintf("<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>Session %s</title></head><body><h1>Session %s</h1></body></html>\n", id, id))
	}
}

// capExport bounds the artifact at maxExportBytes, backing up to a UTF-8 rune
// boundary so a truncated share file never ends in the middle of a code point.
func capExport(data []byte) []byte {
	if len(data) <= maxExportBytes {
		return data
	}
	limit := maxExportBytes - len(exportTruncatedMarker)
	for limit > 0 && !utf8.RuneStart(data[limit]) {
		limit--
	}
	out := make([]byte, 0, maxExportBytes)
	out = append(out, data[:limit]...)
	out = append(out, exportTruncatedMarker...)
	return out
}
