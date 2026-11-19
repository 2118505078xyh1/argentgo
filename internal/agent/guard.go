package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// dedupe is the repeat guard: an identical (tool, normalized args) call that
// already really ran max times in this run is answered with an error result
// instead of touching the workspace again. Models that get stuck re-reading the
// same file would otherwise burn the whole turn budget on one call.
type dedupe struct {
	max    int
	counts map[string]int
}

func newDedupe(maxRepeats int) *dedupe {
	if maxRepeats <= 0 {
		maxRepeats = defaultMaxRepeats
	}
	return &dedupe{max: maxRepeats, counts: make(map[string]int)}
}

// NormalizeArgs canonicalizes a raw JSON argument object so two spellings of
// the same call compare equal: object keys are sorted recursively and
// whitespace is dropped. Non-JSON arguments are whitespace-collapsed.
func NormalizeArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}

// wouldBlock reports whether the next identical call would be blocked, without
// recording it. Used to decide whether a batch may run in parallel.
func (d *dedupe) wouldBlock(name, args string) bool {
	if d == nil || d.max <= 0 {
		return false
	}
	return d.counts[name+"\x00"+NormalizeArgs(args)]+1 > d.max
}

// check records one request and reports whether it must be answered with an
// error result instead of executed.
func (d *dedupe) check(name, args string) (blocked bool, message string) {
	if d == nil || d.max <= 0 {
		return false, ""
	}
	key := name + "\x00" + NormalizeArgs(args)
	d.counts[key]++
	if d.counts[key] <= d.max {
		return false, ""
	}
	return true, fmt.Sprintf(
		"[duplicate call blocked] %s was already called %d times with the same arguments in this run; use the earlier result instead of calling it again",
		name, d.max)
}
