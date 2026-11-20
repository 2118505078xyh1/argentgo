package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// retentionDataLimit leaves headroom under maxLineLen for the record envelope
// and the small _retention field SetRetention adds. A payload above the limit
// is refused rather than producing a JSONL line the reader would skip as
// oversized.
const retentionDataLimit = maxLineLen - 8192

// RetentionFromKind maps a record kind to its default retention hint. The
// policy is deliberately small and automatic, so callers do not have to
// remember to stamp each important record:
//
//   - checkpoint, summary, compaction: durable context anchors. They carry the
//     state needed to resume or explain a run, so they survive compaction and
//     are seeded into child sessions (RetainAlways).
//   - title: cheap orientation for a subagent, but not worth pinning in the
//     parent thread; propagate to children only (RetainInChildren).
//   - everything else (messages, tools, attempts, progress, stats, usage,
//     header, unknown kinds): RetainDefault. Read and write behaviour stays
//     exactly as it was before these hints existed.
//
// Matching is case-insensitive and trims surrounding whitespace, so kinds
// authored outside this package still get the sensible default.
func RetentionFromKind(kind string) Retention {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case KindCheckpoint, KindSummary, KindCompaction:
		return RetainAlways
	case KindTitle:
		return RetainInChildren
	default:
		return RetainDefault
	}
}

// StampIfImportant applies RetentionFromKind to an already-encoded payload and
// returns the bytes to write. Default kinds are returned byte-identical, so
// ordinary message/tool writes are unchanged. If an important kind's payload is
// not a JSON object, SetRetention refuses to mark it and the original bytes are
// returned rather than sacrificing the payload to attach a hint.
func StampIfImportant(kind string, data []byte) []byte {
	r := RetentionFromKind(kind)
	if r == RetainDefault {
		return data
	}
	stamped, err := SetRetention(data, r)
	if err != nil {
		return data
	}
	return stamped
}

// AppendWithRetention writes a pre-serialized record with an explicit retention
// hint. The payload is bounded and passed through the shared JSON redactor
// before stamping, then written through AppendRecord, so ids, branching and
// durability behave exactly like any other append.
//
// data must be a JSON object envelope (or empty/null): SetRetention rejects
// other shapes rather than rewriting a payload it cannot safely preserve.
func (s *Session) AppendWithRetention(kind string, data []byte, r Retention) (Record, error) {
	if len(data) > retentionDataLimit {
		return Record{}, fmt.Errorf("session: retention payload too large: %d bytes (limit %d)", len(data), retentionDataLimit)
	}
	cleaned := data
	if len(bytes.TrimSpace(data)) > 0 {
		cleaned = []byte(security.RedactJSON(data))
	}
	stamped, err := SetRetention(cleaned, r)
	if err != nil {
		return Record{}, err
	}
	if len(stamped) > maxLineLen {
		return Record{}, fmt.Errorf("session: retention payload too large after stamping: %d bytes", len(stamped))
	}
	return s.AppendRecord(kind, json.RawMessage(stamped))
}
