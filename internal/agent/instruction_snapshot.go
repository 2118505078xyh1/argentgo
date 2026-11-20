package agent

// This file implements a layered instruction snapshot that a parent session can
// pass down to its subagents. The idea follows Codex's "layered instruction
// snapshot": the app/memory layer resolves the user, thread and project
// instructions once, freezes them into a deterministic, bounded value, and a
// child subagent inherits that value instead of re-loading or re-resolving it.
//
// Deliberately agent-local: the layers arrive as []string and this file never
// imports internal/memory (or any I/O). The wiring layer
// (internal/app, which owns memory.Load + memory.Block) is responsible for
// turning instruction files into the three []string layers and passing them to
// BuildSnapshot. Keeping the type a plain value makes it cheap to copy into a
// child's SubagentConfig and to use as a cache key via Fingerprint.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxSnapshotBytes is the total byte budget shared by all three layers of a
// snapshot. It matches memory's 32 KiB combined budget so inheriting a snapshot
// cannot inflate a child's system prompt beyond what the parent already pays.
const MaxSnapshotBytes = 32 * 1024

// InheritanceNote is appended to the Thread layer by Inherit so a child can see
// that its instructions were handed down from a parent session rather than
// resolved locally. It is a fixed plain-text marker (no I/O, no paths).
const InheritanceNote = "inherited from parent session"

// InstructionSnapshot is an immutable-by-convention, bounded snapshot of the
// instructions a session has resolved. Layers hold plain text entries; Bytes is
// the total number of stored bytes across all layers and is always recomputed
// by the constructors below.
//
// Callers should treat the slices as read-only. Clone (or Inherit / Merge, which
// clone internally) produces an independent copy before any mutation.
type InstructionSnapshot struct {
	User    []string
	Project []string
	Thread  []string
	Bytes   int
}

// All returns every entry in resolution order: user, then thread, then project.
// The returned slice is freshly allocated so a caller cannot mutate the
// snapshot through it.
func (s InstructionSnapshot) All() []string {
	out := make([]string, 0, len(s.User)+len(s.Thread)+len(s.Project))
	out = append(out, s.User...)
	out = append(out, s.Thread...)
	out = append(out, s.Project...)
	return out
}

// Clone returns a deep copy: every layer is a new slice, so mutating the copy
// (including its entries) never changes the source.
func (s InstructionSnapshot) Clone() InstructionSnapshot {
	return InstructionSnapshot{
		User:    cloneStrings(s.User),
		Project: cloneStrings(s.Project),
		Thread:  cloneStrings(s.Thread),
		Bytes:   s.Bytes,
	}
}

// Fingerprint returns a stable SHA-256 hex digest of the snapshot's content.
// Layer names and per-entry byte lengths are mixed in so that moving a string
// between layers (or changing its boundaries) changes the key. It is suitable
// as a cache key for a validated/applied instruction snapshot.
func (s InstructionSnapshot) Fingerprint() string {
	h := sha256.New()
	layers := [...]struct {
		name string
		vals []string
	}{
		{"user", s.User},
		{"thread", s.Thread},
		{"project", s.Project},
	}
	for _, layer := range layers {
		for _, v := range layer.vals {
			// Length-prefixing with a NUL terminator is unambiguous and
			// deterministic without allocating a joined intermediate string.
			fmt.Fprintf(h, "%s:%d:%s\x00", layer.name, len(v), v)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// BuildSnapshot scrubs, bounds and freezes the three resolved layers into a
// snapshot. The total byte budget (MaxSnapshotBytes) is shared across layers;
// entries are kept in All() order (user, thread, project) and clipped on a UTF-8
// rune boundary if they would cross it. This is pure: no files, no environment,
// no clock.
func BuildSnapshot(user, project, thread []string) InstructionSnapshot {
	return normalizeBudget(user, project, thread, MaxSnapshotBytes)
}

// Inherit returns the snapshot a child subagent should receive: a deep copy of
// the parent's instructions with a bounded inheritance marker appended to the
// Thread layer. The marker's bytes are reserved first, so it is always present
// even when the parent snapshot is at the size cap; older thread entries are
// clipped/dropped to make room rather than losing the inheritance signal.
func (s InstructionSnapshot) Inherit() InstructionSnapshot {
	note := scrubEntry(InheritanceNote)
	budget := MaxSnapshotBytes - len(note)
	if budget < 0 {
		budget = 0
	}
	child := normalizeBudget(s.User, s.Project, s.Thread, budget)
	child.Thread = append(child.Thread, note)
	child.Bytes += len(note)
	return child
}

// MergeSnapshots composes a parent snapshot with child-specific instructions.
// Each layer is concatenated parent-first, de-duplicated by exact entry content
// (preserving first occurrence), then re-bounded and assigned fresh Bytes.
func MergeSnapshots(parent, additional InstructionSnapshot) InstructionSnapshot {
	user := mergeLayer(parent.User, additional.User)
	project := mergeLayer(parent.Project, additional.Project)
	thread := mergeLayer(parent.Thread, additional.Thread)
	return normalizeBudget(user, project, thread, MaxSnapshotBytes)
}

// mergeLayer appends b's entries to a, skipping any content already present.
func mergeLayer(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	out = append(out, a...)
	seen := make(map[string]bool, len(out))
	for _, v := range out {
		seen[v] = true
	}
	for _, v := range b {
		if !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}

// normalizeBudget is the single place that scrubs and bounds layers. It always
// allocates fresh slices, which gives Clone-like isolation to every public
// constructor. Entries are processed in All() order; an entry larger than the
// remaining room is clipped at a rune boundary, and empty/whitespace-only
// entries are dropped.
func normalizeBudget(user, project, thread []string, budget int) InstructionSnapshot {
	if budget < 0 {
		budget = 0
	}
	var out [3][]string
	layers := [3][]string{user, thread, project} // All() order
	used := 0
	for li, entries := range layers {
		for _, raw := range entries {
			entry := scrubEntry(raw)
			if entry == "" || used >= budget {
				continue
			}
			entry = clipRunes(entry, budget-used)
			if entry == "" {
				continue
			}
			out[li] = append(out[li], entry)
			used += len(entry)
		}
	}
	return InstructionSnapshot{
		User:    out[0],
		Thread:  out[1],
		Project: out[2],
		Bytes:   used,
	}
}

// scrubEntry is a minimal, deterministic scrub: drop NUL, C0 control characters
// (except tab/newline/carriage-return) and DEL, then trim surrounding
// whitespace. It intentionally stores plain text without attempting secret
// redaction - that belongs to the caller's instruction resolution and the
// audit layer. WriteRune also normalises malformed UTF-8 to U+FFFD, so the
// result is always valid UTF-8 before clipping.
func scrubEntry(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 0:
			continue
		case r == 0x7f:
			continue
		case r < 0x20 && r != '\t' && r != '\n' && r != '\r':
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// clipRunes shortens s to at most limit bytes without splitting a UTF-8 rune.
// The caller guarantees s is valid UTF-8 and len(s) > limit > 0.
func clipRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	k := limit
	for k > 0 && !utf8.RuneStart(s[k]) {
		k--
	}
	return s[:k]
}

// cloneStrings copies a string slice; nil stays nil so empty layers round-trip
// without allocating.
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
