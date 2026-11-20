package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// retentionKey is the reserved field SetRetention writes into a record's JSON
// data envelope. Keeping retention in the existing Data blob means no schema
// change to Record and no extra JSONL column, and messageEntry already copies
// Data verbatim into Entry.Data so the hint reaches the conversation view.
const retentionKey = "_retention"

// Retention is a per-record hint about how long a record must live. It governs
// two independent lifetimes: compaction of the parent thread and propagation
// into child sessions (subagents).
type Retention uint8

const (
	// RetainDefault is normal behaviour: the record may be compacted away and
	// is not copied into child sessions. Records that never carried the hint
	// also read back as RetainDefault.
	RetainDefault Retention = iota
	// RetainAlways keeps the record across compaction AND propagates it to
	// child sessions.
	RetainAlways
	// RetainInThread keeps the record across compaction only; it is not
	// propagated to child sessions.
	RetainInThread
	// RetainInChildren propagates the record to child sessions only; it may
	// still be compacted away in the parent thread.
	RetainInChildren
)

// String implements fmt.Stringer. It returns the stable lowercase wire value
// used by SetRetention, or a descriptive form for an out-of-range value.
func (r Retention) String() string {
	switch r {
	case RetainDefault:
		return "default"
	case RetainAlways:
		return "always"
	case RetainInThread:
		return "in_thread"
	case RetainInChildren:
		return "in_children"
	default:
		return fmt.Sprintf("Retention(%d)", uint8(r))
	}
}

// SurvivesCompaction reports whether r must be preserved across compaction.
func (r Retention) SurvivesCompaction() bool {
	return r == RetainAlways || r == RetainInThread
}

// SurvivesChildren reports whether r must be seeded into child sessions.
func (r Retention) SurvivesChildren() bool {
	return r == RetainAlways || r == RetainInChildren
}

// parseRetention reads a stored wire value. It is deliberately permissive
// (trimmed, case-folded) so hand-written or older records still read back, and
// it falls back to RetainDefault for anything unrecognised.
func parseRetention(text string) Retention {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "always":
		return RetainAlways
	case "in_thread":
		return RetainInThread
	case "in_children":
		return RetainInChildren
	default:
		return RetainDefault
	}
}

// SetRetention injects r into a record's JSON data envelope under retentionKey,
// preserving every other field. An empty data slice is treated as an empty
// object. A non-empty slice that is not a JSON object (or is malformed) is an
// error: retaining a payload whose other fields cannot be preserved would be
// worse than refusing to mark it.
func SetRetention(data []byte, r Retention) ([]byte, error) {
	fields := make(map[string]json.RawMessage)
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, fmt.Errorf("session: set retention: parse data: %w", err)
		}
	}
	if fields == nil { // JSON "null" unmarshals to a nil map.
		fields = make(map[string]json.RawMessage)
	}

	value, err := json.Marshal(r.String())
	if err != nil {
		return nil, fmt.Errorf("session: set retention: encode value: %w", err)
	}
	fields[retentionKey] = value

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("session: set retention: encode data: %w", err)
	}
	return out, nil
}

// GetRetention reads the retention hint from a record's JSON data envelope.
// Missing, malformed, non-object, or unrecognised values all mean
// RetainDefault, so records written before this hint existed behave normally.
func GetRetention(data []byte) Retention {
	var fields map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &fields) != nil || fields == nil {
		return RetainDefault
	}
	raw, ok := fields[retentionKey]
	if !ok {
		return RetainDefault
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return RetainDefault
	}
	return parseRetention(text)
}

// SelectForCompaction returns the records that must survive compaction: those
// marked RetainAlways or RetainInThread, regardless of where the compactor's
// cut lands. It is a pure helper; the caller decides how to merge the result
// into the kept suffix.
//
// budget caps how many records are returned (it is a count, not a token
// estimate). A non-positive budget means no cap. When the cap forces a choice,
// the most recently occurring flagged records win, mirroring KeepForBudget's
// "newest context survives" rule. The result is in original record order.
func SelectForCompaction(records []Record, budget int) []Record {
	flagged := make([]Record, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		if !GetRetention(records[i].Data).SurvivesCompaction() {
			continue
		}
		flagged = append(flagged, records[i])
		if budget > 0 && len(flagged) == budget {
			break
		}
	}
	if len(flagged) == 0 {
		return nil
	}
	for i, j := 0, len(flagged)-1; i < j; i, j = i+1, j-1 {
		flagged[i], flagged[j] = flagged[j], flagged[i]
	}
	return flagged
}

// SelectForChildren returns the records that must be seeded into child
// sessions: those marked RetainAlways or RetainInChildren, in original record
// order. RetainInThread is intentionally excluded — it survives compaction in
// the parent but must not leak into subagent context.
func SelectForChildren(records []Record) []Record {
	var out []Record
	for _, record := range records {
		if GetRetention(record.Data).SurvivesChildren() {
			out = append(out, record)
		}
	}
	return out
}
