package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// exportEntry mirrors the JSON schema of session's unexported exportEntry.
// Matching the field order and tags exactly means re-marshaling an unfiltered
// transcript with json.MarshalIndent(..., "", "  ") is byte-for-byte identical
// to what session.ExportProject produced (including the trailing newline).
type exportEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Tool string `json:"tool"`
	TS   string `json:"ts"`
	Kind string `json:"kind"`
	Note string `json:"note,omitempty"`
}

// ExportFilter holds the optional predicates applied to a structured export.
// Every empty field is ignored, so the zero value keeps every entry.
type ExportFilter struct {
	Since string   // inclusive lower bound on the entry timestamp
	Until string   // inclusive upper bound on the entry timestamp
	Types []string // accepted entry kinds or roles, case-insensitive
}

// active reports whether any predicate is set. Values are trimmed so a flag
// given with an empty value (e.g. --since=) is treated as no constraint.
func (f ExportFilter) active() bool {
	return strings.TrimSpace(f.Since) != "" || strings.TrimSpace(f.Until) != "" || len(f.Types) > 0
}

// filterExportEntries returns the entries satisfying every predicate: a
// timestamp within [since, until] (inclusive) and a kind or role present in
// types (case-insensitive). Time values are parsed by parseAuditTime, so RFC
// 3339, plain dates and CJK forms (年月日 / 时分秒) all work. An entry whose
// timestamp cannot be parsed is dropped whenever a time bound is active.
// The input slice is never modified and the original order is preserved.
func filterExportEntries(entries []exportEntry, since, until string, types []string) []exportEntry {
	sinceTS, hasSince := parseAuditTime(since)
	untilTS, hasUntil := parseAuditTime(until)
	wanted := make(map[string]struct{}, len(types))
	for _, typ := range types {
		if trimmed := strings.TrimSpace(typ); trimmed != "" {
			wanted[strings.ToLower(trimmed)] = struct{}{}
		}
	}

	out := make([]exportEntry, 0, len(entries))
	for _, entry := range entries {
		if len(wanted) > 0 {
			_, kindOK := wanted[strings.ToLower(entry.Kind)]
			_, roleOK := wanted[strings.ToLower(entry.Role)]
			if !kindOK && !roleOK {
				continue
			}
		}
		if hasSince || hasUntil {
			ts, ok := parseAuditTime(entry.TS)
			if !ok {
				continue
			}
			if hasSince && ts.Before(sinceTS) {
				continue
			}
			if hasUntil && ts.After(untilTS) {
				continue
			}
		}
		out = append(out, entry)
	}
	return out
}

// isFilterableExport reports whether an export format is structured (JSON or
// YAML), i.e. whether the CLI can filter it by re-parsing its entries. HTML and
// Markdown are single rendered blobs and cannot be filtered structurally here.
func isFilterableExport(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "json") || isYAMLExport(format)
}

// filterExportArtifact applies f to a rendered JSON or YAML export. YAML is a
// leading comment followed by a JSON body; the comment is stripped, the body is
// filtered and the comment is restored. When f is inactive, or the format is
// not filterable, the original bytes are returned verbatim so unfiltered and
// text exports stay byte-for-byte unchanged.
func filterExportArtifact(data []byte, format string, f ExportFilter) ([]byte, error) {
	if !f.active() || !isFilterableExport(format) {
		return data, nil
	}

	body := data
	var header []byte
	if isYAMLExport(format) {
		header = []byte(yamlExportHeader)
		if !bytes.HasPrefix(data, header) {
			return nil, fmt.Errorf("session: malformed YAML export: missing header")
		}
		body = bytes.TrimPrefix(data, header)
	}

	var entries []exportEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("session: parse export for filtering: %w", err)
	}
	entries = filterExportEntries(entries, f.Since, f.Until, f.Types)
	filtered, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("session: encode filtered export: %w", err)
	}
	filtered = append(filtered, '\n')
	if header != nil {
		filtered = append(header, filtered...)
	}
	return filtered, nil
}
