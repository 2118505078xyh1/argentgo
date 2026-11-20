package memory

// Source-provenance diagnostics for instruction documents.
//
// The loaders (LoadWithBoundary, LoadIn, LoadForDirs) attach a Source path to
// every document but do not expose how that path maps to a scope or to the
// project boundary. This file adds a pure diagnostic view over the already
// loaded documents: it reports, for each instruction, where it came from, how
// deep it is, which directory it scopes, and whether it is confined to a
// subtree ("boundary limited") rather than applying across the whole set.
//
// Like DescribeIgnoreRules, this is a display/debug aid only: it performs no
// disk I/O, injects nothing into the prompt, and never mutates its input. It is
// additive — no loader and no existing exported function changes.

import (
	"path/filepath"
	"strings"
)

// SourceInfo is the provenance of one instruction document, produced by
// InstructionSources. It is a pure value intended for UI/debug display.
type SourceInfo struct {
	// Source is the cleaned source path, copied verbatim from the Instruction
	// (the loaders produce absolute paths). A doc with an empty/blank Source is
	// skipped by InstructionSources and never appears here.
	Source string
	// Name is the base name of the source file: AGENTS.md, CLAUDE.md,
	// AGENTS.override.md, etc. It lets a UI show which discovery slot a rule
	// came from without parsing the path again.
	Name string
	// Depth is the directory-separator count of the source's directory part,
	// the same metric OrderInstructions uses to rank nesting. Larger values
	// mean a deeper, more specific source.
	Depth int
	// ScopeDir is the directory the instruction applies to: the parent of
	// Source. The rule governs this directory and everything below it.
	ScopeDir string
	// BoundaryLimited reports whether this instruction is confined to a
	// subtree. It is true when another document in the same input set has a
	// scope directory that is a STRICT ancestor of ScopeDir — i.e. a more
	// general rule exists above it, so this one only applies inside its own
	// subtree boundary. A root-level instruction (no ancestor in the set), a
	// lone instruction, and instructions sharing the same directory are never
	// limited. This is a set-relative diagnostic, not a loader/trust fact: it
	// is derived solely from the paths supplied to InstructionSources.
	BoundaryLimited bool
}

// InstructionSources returns a displayable provenance record for every
// instruction with a non-blank Source, preserving input order. It is the pure
// provenance companion to LoadWithBoundary/RelevantInstructions/OrderInstructions:
// it performs no disk I/O, never mutates docs, and returns a fresh slice (nil
// for nil/empty input or when every source is blank).
//
// Path handling matches the rest of the package: paths are cleaned, directory
// containment and equality are case-insensitive on Windows (samePath /
// withinWorkspace), and Depth reuses pathDepth so the two diagnostics agree on
// what "deeper" means. When the same directory contributes more than one file
// (AGENTS.md alongside CLAUDE.md), all of them share one ScopeDir and none is
// marked BoundaryLimited relative to the other.
func InstructionSources(docs []Instruction) []SourceInfo {
	if len(docs) == 0 {
		return nil
	}

	type entry struct {
		info     SourceInfo
		scopeDir string
	}
	entries := make([]entry, 0, len(docs))
	for _, d := range docs {
		if strings.TrimSpace(d.Source) == "" {
			continue
		}
		path := filepath.Clean(d.Source)
		scopeDir := filepath.Dir(path)
		entries = append(entries, entry{
			info: SourceInfo{
				Source:   path,
				Name:     filepath.Base(path),
				Depth:    pathDepth(path),
				ScopeDir: scopeDir,
			},
			scopeDir: scopeDir,
		})
	}
	if len(entries) == 0 {
		return nil
	}

	out := make([]SourceInfo, len(entries))
	for i := range entries {
		out[i] = entries[i].info
		// A strict ancestor scope in the set makes this instruction subtree-
		// scoped. Equal directories are excluded so sibling files in one dir
		// do not mark each other as limited.
		for j := range entries {
			if i == j {
				continue
			}
			if !samePath(entries[i].scopeDir, entries[j].scopeDir) &&
				withinWorkspace(entries[i].scopeDir, entries[j].scopeDir) {
				out[i].BoundaryLimited = true
				break
			}
		}
	}
	return out
}
