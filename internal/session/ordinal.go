package session

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// seqKey is the reserved field SetSeq writes into a record's JSON data
// envelope. Like retentionKey, it lives in the existing Data blob, so no
// Record schema change and no extra JSONL column are needed, and Data keeps
// reaching the conversation view verbatim.
//
// A record's ordinal is a monotonic append-order counter. Unlike TS it cannot
// collide when several records are written inside one clock tick, which makes
// "newest" and fork points deterministic.
const seqKey = "_seq"

// GetSeq reads the explicit ordinal from a record's JSON data envelope. It
// reports (seq, true) only for a JSON object whose seqKey holds a non-negative
// integer that fits in int64. Missing, malformed, non-object, null, fractional,
// string and negative values all report (0, false), so records written before
// ordinals existed (and hand-written ones) fall back to derived order.
func GetSeq(data []byte) (int64, bool) {
	var fields map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(data, &fields) != nil || fields == nil {
		return 0, false
	}
	raw, ok := fields[seqKey]
	if !ok {
		return 0, false
	}
	// json.Unmarshal(null, &int64) succeeds and leaves zero, so reject it here.
	if string(bytes.TrimSpace(raw)) == "null" {
		return 0, false
	}
	var seq int64
	if err := json.Unmarshal(raw, &seq); err != nil || seq < 0 {
		return 0, false
	}
	return seq, true
}

// SetSeq injects seq into a record's JSON data envelope under seqKey,
// preserving every other field. An empty data slice is treated as an empty
// object. A non-empty slice that is not a JSON object (or is malformed) is an
// error: replacing a payload whose other fields cannot be preserved would be
// worse than refusing to stamp it. Negative ordinals are rejected because an
// ordinal must be usable as a monotonic replay point.
func SetSeq(data []byte, seq int64) ([]byte, error) {
	if seq < 0 {
		return nil, fmt.Errorf("session: set seq: negative ordinal %d", seq)
	}
	fields := make(map[string]json.RawMessage)
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 {
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, fmt.Errorf("session: set seq: parse data: %w", err)
		}
	}
	if fields == nil { // JSON "null" unmarshals to a nil map.
		fields = make(map[string]json.RawMessage)
	}
	value, err := json.Marshal(seq)
	if err != nil {
		return nil, fmt.Errorf("session: set seq: encode value: %w", err)
	}
	fields[seqKey] = value

	out, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("session: set seq: encode data: %w", err)
	}
	return out, nil
}

// SeqOf reports the explicit ordinal carried by r, if any.
func SeqOf(r Record) (int64, bool) { return GetSeq(r.Data) }

// RecordSeqs returns one effective ordinal per record, in the same order.
// Records carrying an explicit _seq use it; records without one derive their
// ordinal from their zero-based array index. The derived fallback lets fork and
// replay helpers work on legacy transcripts that predate ordinals.
//
// Mixing explicit and derived ordinals is only meaningful when the explicit
// ones increase in file order, as they do for an append-only session. Writers
// should stamp every new record with SetSeq instead of relying on the fallback.
func RecordSeqs(records []Record) []int64 {
	seqs := make([]int64, len(records))
	for i := range records {
		if seq, ok := GetSeq(records[i].Data); ok {
			seqs[i] = seq
		} else {
			seqs[i] = int64(i)
		}
	}
	return seqs
}

// NewestBySeq returns the record with the highest ordinal. An explicit ordinal
// is authoritative: any record carrying _seq is newer than a legacy record
// without one, and among stamped records the greater seq wins.
//
// When neither candidate carries a seq (or their seqs tie), the tiebreak is TS
// then ID — the same "later timestamp, then greater id" rule Store.List uses.
// That keeps "newest" deterministic when several records share one second, the
// exact ambiguity that makes timestamp-only resume flaky. An empty input
// returns the zero Record.
func NewestBySeq(records []Record) Record {
	if len(records) == 0 {
		return Record{}
	}
	newest := records[0]
	newestSeq, newestHasSeq := SeqOf(newest)
	for _, record := range records[1:] {
		seq, hasSeq := SeqOf(record)
		switch {
		case hasSeq != newestHasSeq:
			// A stamped record always outranks an un-stamped one.
			if hasSeq {
				newest, newestSeq, newestHasSeq = record, seq, hasSeq
			}
		case hasSeq && seq != newestSeq:
			if seq > newestSeq {
				newest, newestSeq = record, seq
			}
		default:
			// Both lack seq, or their seqs tie.
			if newerByTimeAndID(record, newest) {
				newest, newestSeq, newestHasSeq = record, seq, hasSeq
			}
		}
	}
	return newest
}

// newerByTimeAndID breaks ties the way Store.List does: later timestamp wins,
// then the lexicographically greater id.
func newerByTimeAndID(a, b Record) bool {
	if a.TS.Equal(b.TS) {
		return a.ID > b.ID
	}
	return a.TS.After(b.TS)
}

// ForkBeforeSeq returns the leading records whose effective ordinal is < seq,
// in their original order. It is the replay prefix for a fork at an ordinal:
// everything strictly before the target, nothing from the target onward.
// Effective ordinals come from RecordSeqs, so legacy records without _seq are
// ordered by array index and remain forkable.
//
// The result is a copy, never a slice of the caller's backing array. An empty
// prefix (target at or below the first ordinal) returns nil; a target above
// every ordinal returns a copy of the whole slice. Callers should keep ordinals
// increasing in file order — the prefix stops at the first ordinal >= seq.
func ForkBeforeSeq(records []Record, seq int64) []Record {
	seqs := RecordSeqs(records)
	i := 0
	for i < len(records) && seqs[i] < seq {
		i++
	}
	if i == 0 {
		return nil
	}
	out := make([]Record, i)
	copy(out, records[:i])
	return out
}
