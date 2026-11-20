package memory

import (
	"path/filepath"
	"sort"
	"strings"
)

// OrderInstructions returns docs reordered for cascade-style injection:
// root-to-cwd by the directory depth of each Source path, so instructions
// closest to the working directory come last and, under a "later wins"
// renderer, override the more general rules above them.
//
// It is the pure ordering companion to LoadWithBoundary/RelevantInstructions:
// it performs no disk I/O, never mutates docs, and returns a fresh slice (nil
// for empty input). The ordering rules are:
//
//   - Rank by directory depth: a deeper Source directory is nearer to cwd and
//     is placed later. Depth is the number of separators in the cleaned
//     directory part, so the ranking reflects real nesting as long as all
//     sources share a workspace root (as loader-produced docs do).
//   - Same-depth entries are sorted by their normalized Source path text,
//     with a stable tie-break, for deterministic output.
//   - Duplicate sources (by the normalizePath key used elsewhere in this
//     package, case-folded on Windows) are removed, keeping the first
//     occurrence in input order.
//   - Instructions with an empty/blank Source are dropped: they have no path
//     to rank, matching RelevantInstructions' handling of missing sources.
//
// This is deliberately the opposite end of the ordering spectrum from Block,
// which documents "most specific first, earlier wins". OrderInstructions does
// not change Block or any loader; it is an additive helper for callers whose
// renderer uses a "last wins" convention.
func OrderInstructions(docs []Instruction) []Instruction {
	if len(docs) == 0 {
		return nil
	}

	type ranked struct {
		inst  Instruction
		depth int
		key   string
	}

	items := make([]ranked, 0, len(docs))
	seen := make(map[string]bool, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.Source) == "" {
			continue
		}
		path := filepath.Clean(d.Source)
		key := normalizePath(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, ranked{inst: d, depth: pathDepth(path), key: key})
	}
	if len(items) == 0 {
		return nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].depth != items[j].depth {
			return items[i].depth < items[j].depth
		}
		return items[i].key < items[j].key
	})

	out := make([]Instruction, len(items))
	for i, it := range items {
		out[i] = it.inst
	}
	return out
}

// pathDepth returns the number of directory separators in the cleaned path's
// directory part. Deeper directories (closer to a working directory beneath a
// shared root) yield a larger number, which OrderInstructions uses to place
// nearer instructions later.
func pathDepth(path string) int {
	return strings.Count(filepath.Dir(path), string(filepath.Separator))
}
