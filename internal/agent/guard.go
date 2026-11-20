package agent

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// defaultRepeatWindow is how many recent tool calls the repeat guard keeps in
// its sliding window. Only repeats inside this window count toward the block
// threshold, so a burst of duplicates long ago no longer blocks a legitimate
// retry much later in the same run.
const defaultRepeatWindow = 10

// dedupe is the repeat guard. Two independent signals feed the same decision:
//
//   - the argument window counts (tool, normalized args) pairs in the most
//     recent calls; more than max repeats of one signature in that window is a
//     blocked "stuck on the same call" loop;
//   - the output window groups recent results of one tool by output hash. When
//     more than max distinct argument sets produced the same output, the model
//     is making only pseudo-progress (e.g. re-reading different files that all
//     return the same body) and the next call is blocked too.
//
// A call that is answered with an error result never touches the workspace
// again, which keeps a stuck model from burning the whole turn budget.
type dedupe struct {
	max    int
	window int

	// args-based ring buffer, holding normalizedKey values.
	ring   []string
	head   int
	filled int
	counts map[string]int

	// output-based ring buffer, one per tool name.
	outputs map[string]*outputWindow
}

// outputWindow holds the most recent (normalized args, output hash) pairs for
// one tool. The hash stands in for the output so a large result is not retained.
type outputWindow struct {
	entries []outputEntry
	head    int
	filled  int
}

type outputEntry struct {
	normalized string
	hash       string
}

func newDedupe(maxRepeats int) *dedupe {
	return newDedupeWindow(maxRepeats, defaultRepeatWindow)
}

// newDedupeWindow is the windowed constructor. newDedupe is the thin wrapper
// used by the rest of the package; this form exists so tests can pick a small
// window and exercise eviction without a long scripted run.
func newDedupeWindow(maxRepeats, window int) *dedupe {
	if maxRepeats <= 0 {
		maxRepeats = defaultMaxRepeats
	}
	if window <= 0 {
		window = defaultRepeatWindow
	}
	return &dedupe{
		max:     maxRepeats,
		window:  window,
		ring:    make([]string, window),
		counts:  make(map[string]int),
		outputs: make(map[string]*outputWindow),
	}
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

// normalizedKey builds the map key shared by both the cached-key and the raw
// argument paths of the guard. Keeping it in one place guarantees the two agree
// byte-for-byte.
func normalizedKey(name, normalized string) string {
	return name + "\x00" + normalized
}

// recordNormalized appends (name, normalized) to the argument window, evicting
// the oldest entry when the window is full, and returns the resulting number of
// identical signatures currently in the window.
func (d *dedupe) recordNormalized(name, normalized string) int {
	key := normalizedKey(name, normalized)
	if d.filled == d.window {
		old := d.ring[d.head]
		d.counts[old]--
		if d.counts[old] <= 0 {
			delete(d.counts, old)
		}
	} else {
		d.filled++
	}
	d.ring[d.head] = key
	d.head = (d.head + 1) % d.window
	d.counts[key]++
	return d.counts[key]
}

// outputWouldBlock reports whether recent calls to name have returned the same
// output for more than max distinct argument sets. That pattern means the model
// is varying the request but the tool keeps answering the same thing, i.e. a
// pseudo-progress loop that the argument-only signature cannot see.
func (d *dedupe) outputWouldBlock(name string) bool {
	ow := d.outputs[name]
	if ow == nil || ow.filled == 0 {
		return false
	}
	byHash := make(map[string]map[string]bool)
	for i := 0; i < ow.filled; i++ {
		idx := (ow.head - 1 - i + len(ow.entries)) % len(ow.entries)
		entry := ow.entries[idx]
		if entry.hash == "" {
			continue
		}
		args := byHash[entry.hash]
		if args == nil {
			args = make(map[string]bool)
			byHash[entry.hash] = args
		}
		args[entry.normalized] = true
	}
	for _, args := range byHash {
		if len(args) > d.max {
			return true
		}
	}
	return false
}

// recordOutput attaches the result of an executed call to the output window.
// It is a no-op for a nil/disabled guard and for output the caller cannot
// supply; it never blocks anything on its own.
func (d *dedupe) recordOutput(name, normalized, output string) {
	if d == nil || d.max <= 0 {
		return
	}
	ow := d.outputs[name]
	if ow == nil {
		ow = &outputWindow{entries: make([]outputEntry, d.window)}
		d.outputs[name] = ow
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(output))
	ow.entries[ow.head] = outputEntry{normalized: normalized, hash: strconv.FormatUint(h.Sum64(), 16)}
	ow.head = (ow.head + 1) % len(ow.entries)
	if ow.filled < len(ow.entries) {
		ow.filled++
	}
}

// blockedMessage is the single wording for every repeat block. Keeping both
// signals on the same message preserves the transcript contract: callers match
// on "duplicate call blocked" regardless of which signal fired.
func (d *dedupe) blockedMessage(name string) string {
	return fmt.Sprintf(
		"[duplicate call blocked] %s was already called %d times with the same arguments in this run; use the earlier result instead of calling it again",
		name, d.max)
}

// wouldBlockNormalized is wouldBlock for arguments the caller already
// normalized. It projects the argument window one call ahead (accounting for
// an eviction when the window is full) without recording anything.
func (d *dedupe) wouldBlockNormalized(name, normalized string) bool {
	if d == nil || d.max <= 0 {
		return false
	}
	key := normalizedKey(name, normalized)
	projected := d.counts[key] + 1
	if d.filled == d.window && d.ring[d.head] == key {
		// Adding this call evicts an identical oldest entry and adds one back.
		projected--
	}
	return projected > d.max
}

// checkNormalized is check for arguments the caller already normalized. It
// records the call in the argument window and reports whether it must be
// answered with an error result instead of executed.
func (d *dedupe) checkNormalized(name, normalized string) (blocked bool, message string) {
	if d == nil || d.max <= 0 {
		return false, ""
	}
	count := d.recordNormalized(name, normalized)
	if count > d.max || d.outputWouldBlock(name) {
		return true, d.blockedMessage(name)
	}
	return false, ""
}

// wouldBlock reports whether the next identical call would be blocked, without
// recording it. Used to decide whether a batch may run in parallel.
func (d *dedupe) wouldBlock(name, args string) bool {
	if d == nil || d.max <= 0 {
		return false
	}
	return d.wouldBlockNormalized(name, NormalizeArgs(args))
}

// check records one request and reports whether it must be answered with an
// error result instead of executed.
func (d *dedupe) check(name, args string) (blocked bool, message string) {
	if d == nil || d.max <= 0 {
		return false, ""
	}
	return d.checkNormalized(name, NormalizeArgs(args))
}
