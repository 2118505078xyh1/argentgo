package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DecodeKind says whether one physical JSONL line can be trusted.
type DecodeKind int

const (
	DecodeOK DecodeKind = iota
	DecodeSyntax
	DecodeSchema
)

// ErrHardCorruption marks a transcript that cannot be safely repaired: a
// schema error anywhere, or a syntax error on a non-final line.
var ErrHardCorruption = errors.New("session: hard corruption")

// A session transcript is bounded so a damaged or hostile file cannot force an
// unbounded read during validation.
const maxRepairBytes = 64 << 20

// RepairReport summarizes one validation pass over a session transcript.
type RepairReport struct {
	Path          string
	PhysicalLines int
	KeptLines     int
	TornTail      bool
	Repaired      bool
}

// ValidateAndRepair checks a JSONL transcript before a lenient readRecords
// rebuilds the session tree on truncated history. Only a syntax error on the
// LAST physical line is a recoverable torn tail; with repair set it is dropped
// by publishing the valid prefix atomically. A schema error anywhere, or a
// syntax error on a non-final line, is hard corruption and leaves the file
// untouched. A valid file that merely lacks its final newline gets one appended
// in a separate small repair.
func ValidateAndRepair(path string, repair bool) (RepairReport, error) {
	report := RepairReport{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("session: inspect %s: %w", path, err)
	}
	if info.Size() > maxRepairBytes {
		return report, fmt.Errorf("session: repair %s: file too large (%d bytes)", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return report, fmt.Errorf("session: read %s: %w", path, err)
	}
	if len(data) > maxRepairBytes {
		return report, fmt.Errorf("session: repair %s: file too large (%d bytes)", path, len(data))
	}

	lines := strings.Split(string(data), "\n")
	// Drop exactly one trailing empty string so the final physical line is the
	// last real line, while an extra blank line remains visible as a torn tail.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	report.PhysicalLines = len(lines)
	last := len(lines) - 1

	for idx, line := range lines {
		kind, detail := decodeLine(line)
		switch kind {
		case DecodeOK:
			report.KeptLines = idx + 1
		case DecodeSchema:
			return report, fmt.Errorf("%w: %s: line %d: %s",
				ErrHardCorruption, path, idx+1, detail)
		case DecodeSyntax:
			if idx != last {
				return report, fmt.Errorf("%w: %s: line %d: %s",
					ErrHardCorruption, path, idx+1, detail)
			}
			report.TornTail = true
			report.KeptLines = idx
			if !repair {
				return report, nil
			}
			validPrefix := strings.Join(lines[:idx], "\n") + "\n"
			if err := atomicPublishJSONL(path, []byte(validPrefix)); err != nil {
				return report, fmt.Errorf("session: repair torn tail in %s: %w", path, err)
			}
			report.Repaired = true
			return report, nil
		}
	}

	if repair && !strings.HasSuffix(string(data), "\n") {
		if err := appendRepairNewline(path); err != nil {
			return report, fmt.Errorf("session: repair tail in %s: %w", path, err)
		}
		report.Repaired = true
	}
	return report, nil
}

// decodeLine classifies one physical line and explains any rejection. Absent
// id, parent and ts fields are legal because readRecords synthesizes an id and
// leaves the other zero values untouched.
func decodeLine(line string) (DecodeKind, string) {
	trimmed := strings.TrimSpace(line)
	if !json.Valid([]byte(line)) {
		return DecodeSyntax, "invalid JSON syntax"
	}
	if trimmed == "" || trimmed[0] != '{' {
		return DecodeSchema, "record is not a JSON object"
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil || obj == nil {
		return DecodeSchema, "record is not a JSON object"
	}
	kind, ok := jsonString(obj["kind"])
	if !ok {
		return DecodeSchema, "kind is missing"
	}
	if kind == "" {
		return DecodeSchema, "kind must be a non-empty string"
	}
	for _, field := range []string{"id", "parent"} {
		if raw, present := obj[field]; present {
			if _, ok := jsonString(raw); !ok {
				return DecodeSchema, field + " must be a string"
			}
		}
	}
	if raw, present := obj["ts"]; present {
		stamp, ok := jsonString(raw)
		if !ok {
			return DecodeSchema, "ts must be a string"
		}
		if _, err := time.Parse(time.RFC3339, stamp); err != nil {
			return DecodeSchema, "ts must be RFC3339"
		}
	}
	return DecodeOK, ""
}

// jsonString rejects null and non-string scalars, which encoding/json would
// otherwise silently accept into a Go string.
func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

// atomicPublishJSONL stages a complete transcript next to its target, syncs it
// before the rename, and cleans up the staged file on any failure. On Windows
// os.Rename replaces the existing target (MOVEFILE_REPLACE_EXISTING).
func atomicPublishJSONL(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".jsonl-*.tmp")
	if err != nil {
		return fmt.Errorf("stage temp file: %w", err)
	}
	name := tmp.Name()
	published := false
	defer func() {
		if !published {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if info, err := os.Stat(path); err == nil {
		_ = tmp.Chmod(info.Mode().Perm())
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("stage repair: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync staged repair: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staged repair: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("publish repair: %w", err)
	}
	published = true
	syncRepairDir(dir)
	return nil
}

// syncRepairDir is best-effort: Windows reports EINVAL for directory handles
// and some filesystems reject Sync outright, neither of which undoes a rename.
func syncRepairDir(dir string) {
	handle, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = handle.Sync()
	_ = handle.Close()
}

// appendRepairNewline fixes an unterminated final record without rewriting the
// rest of the transcript. It is kept separate from torn-tail repair so a schema
// error can never be masked by a cosmetic rewrite.
func appendRepairNewline(path string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	_, writeErr := file.Write([]byte("\n"))
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append newline: %w", writeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync newline: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close: %w", closeErr)
	}
	return nil
}
