package tools

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// This file holds the pure, read-only layer for multi_edit planning. It does
// no I/O, takes no locks and writes no bytes: the live multi_edit path in
// multi_edit.go is deliberately untouched, so default behaviour stays
// byte-for-byte identical. Wiring these helpers in is a separate, opt-in
// decision (see the report), because cross-file relocation changes edit
// semantics and can silently edit a file the model did not name.

// multiEditBlock is one SEARCH/REPLACE block for a dry run. Path is the file
// the model intended to edit, Old is the SEARCH text and New the REPLACE text.
// Alias normalization (old_text/new_text) and workspace path resolution stay
// with the calling tool, so this remains a plain value.
type multiEditBlock struct {
	Path string
	Old  string
	New  string
}

// multiEditKnownFiles is a read-only snapshot of candidate files, keyed by
// canonical path. See canonicalEditPath for the normalization rule.
type multiEditKnownFiles map[string]string

// multiEditFileResolver supplies the snapshot a dry run needs. Content returns
// a file's current content and whether the file is known; Paths lists every
// file that may be considered when a SEARCH block misses its intended target.
// Implementations must not write to disk.
type multiEditFileResolver interface {
	Content(path string) (string, bool)
	Paths() []string
}

// multiEditSnapshotResolver adapts an in-memory snapshot to the resolver
// interface. It is useful both in tests and for a future caller that has
// already read the candidate files.
type multiEditSnapshotResolver struct {
	files multiEditKnownFiles
}

func (r multiEditSnapshotResolver) Content(path string) (string, bool) {
	content, ok := r.files[canonicalEditPath(path)]
	return content, ok
}

func (r multiEditSnapshotResolver) Paths() []string {
	out := make([]string, 0, len(r.files))
	for path := range r.files {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// canonicalEditPath makes path comparisons stable across callers that use
// either slash or backslash separators. It is purely lexical: no filesystem
// access, so symlinks and CJK names are left alone (CJK bytes are untouched by
// Clean/ToSlash). Callers pass workspace-relative or already-resolved paths.
func canonicalEditPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

// multiEditFileCandidate is a file other than the block's intended target in
// which the SEARCH text was found. ViaRepair reports that only the graded
// repair ladder matched: such a candidate needs an explicit decision before it
// is used, because a fuzzy match is weaker evidence than a literal one.
type multiEditFileCandidate struct {
	Path      string
	ViaRepair bool
}

// multiEditFailureKind classifies why a block cannot be applied as written.
type multiEditFailureKind string

const (
	multiEditEmptySearch multiEditFailureKind = "empty_search"
	multiEditNotFound    multiEditFailureKind = "not_found"
	multiEditNotUnique   multiEditFailureKind = "not_unique"
	multiEditNeedsRepair multiEditFailureKind = "needs_repair"
	multiEditWrongFile   multiEditFailureKind = "wrong_file"
)

// multiEditBlockFailure is one failed block of a dry-run plan. Index is the
// 0-based position in the input batch; Candidates is populated for wrong_file
// and needs_repair failures and is sorted by path for deterministic output.
type multiEditBlockFailure struct {
	Index      int
	Kind       multiEditFailureKind
	Path       string
	Candidates []multiEditFileCandidate
}

// multiEditPlan is the result of a dry run. Applicable holds the 0-based indices
// of blocks that match literally and uniquely in their own file, in input
// order. Failed holds one entry per block that needs a decision. Snapshot holds
// the simulated contents after applying only the Applicable edits in order;
// failed blocks are never simulated, so a later block never sees a speculative
// relocation or fuzzy edit.
type multiEditPlan struct {
	Applicable []int
	Failed     []multiEditBlockFailure
	Snapshot   multiEditKnownFiles
}

// OK reports whether every block can be applied literally as written.
func (p multiEditPlan) OK() bool { return len(p.Failed) == 0 }

// planMultiEdit validates a whole batch in memory and classifies each block.
// It never writes. Blocks are evaluated in order against a snapshot, so a
// successful edit is visible to the blocks that follow it (matching the live
// tool's sequential semantics), while a failed edit is not.
//
// The classification is intentionally conservative:
//   - a unique literal match in the intended file is applicable;
//   - a unique literal match only in another known file is wrong_file;
//   - a match recoverable only through the repair ladder is needs_repair;
//   - several literal matches in the intended file are not_unique (never
//     guessed, mirroring repairReplace);
//   - no match anywhere is not_found; an empty Old is empty_search.
func planMultiEdit(blocks []multiEditBlock, resolver multiEditFileResolver) multiEditPlan {
	plan := multiEditPlan{Snapshot: make(multiEditKnownFiles)}
	for _, path := range resolver.Paths() {
		if content, ok := resolver.Content(path); ok {
			plan.Snapshot[canonicalEditPath(path)] = content
		}
	}

	for i, block := range blocks {
		if block.Old == "" {
			plan.Failed = append(plan.Failed, multiEditBlockFailure{Index: i, Kind: multiEditEmptySearch, Path: block.Path})
			continue
		}

		path := canonicalEditPath(block.Path)
		content, exists := plan.Snapshot[path]
		if exists {
			switch strings.Count(content, block.Old) {
			case 1:
				plan.Applicable = append(plan.Applicable, i)
				plan.Snapshot[path] = strings.Replace(content, block.Old, block.New, 1)
				continue
			case 0:
				// Literal miss: record a recoverable in-file repair separately
				// from a wrong filename, but apply neither without a decision.
				if _, ok := repairReplace(content, block.Old, block.New); ok {
					plan.Failed = append(plan.Failed, multiEditBlockFailure{
						Index: i, Kind: multiEditNeedsRepair, Path: block.Path,
						Candidates: []multiEditFileCandidate{{Path: path, ViaRepair: true}},
					})
					continue
				}
			default:
				plan.Failed = append(plan.Failed, multiEditBlockFailure{Index: i, Kind: multiEditNotUnique, Path: block.Path})
				continue
			}
		}

		candidates := resolveBlockFile(block, plan.Snapshot)
		kind := multiEditNotFound
		for _, c := range candidates {
			if !c.ViaRepair {
				kind = multiEditWrongFile
				break
			}
			kind = multiEditNeedsRepair
		}
		plan.Failed = append(plan.Failed, multiEditBlockFailure{Index: i, Kind: kind, Path: block.Path, Candidates: candidates})
	}
	return plan
}

// resolveBlockFile looks for a SEARCH block in every known file other than its
// intended target, which is how a wrong filename written by the model can be
// recovered. Literal unique matches are returned first, fuzzy (repair-ladder)
// matches after them; each group is sorted by path so callers get stable output
// and can choose to ignore the weaker group entirely.
//
// It is a pure diagnostic: it never edits anything and never picks a winner
// when two files both match, since guessing would change the wrong file.
func resolveBlockFile(block multiEditBlock, knownFiles multiEditKnownFiles) []multiEditFileCandidate {
	if block.Old == "" {
		return nil
	}
	intended := canonicalEditPath(block.Path)
	var exact, fuzzy []multiEditFileCandidate
	for path, content := range knownFiles {
		canon := canonicalEditPath(path)
		if canon == intended {
			continue
		}
		if strings.Count(content, block.Old) == 1 {
			exact = append(exact, multiEditFileCandidate{Path: path})
			continue
		}
		if _, ok := repairReplace(content, block.Old, block.New); ok {
			fuzzy = append(fuzzy, multiEditFileCandidate{Path: path, ViaRepair: true})
		}
	}
	sort.Slice(exact, func(i, j int) bool { return exact[i].Path < exact[j].Path })
	sort.Slice(fuzzy, func(i, j int) bool { return fuzzy[i].Path < fuzzy[j].Path })
	return append(exact, fuzzy...)
}

// multiEditBatchDiagnostic builds a self-consistent diagnostic for a failed
// batch. The instruction always follows the actual write semantics:
//   - atomic (today's multi_edit): no bytes were written, so the whole batch
//     must be re-sent;
//   - graded (a possible future partial writer): matched edits were written,
//     so only failing blocks should be re-sent.
//
// It is deliberately separate from multiEditRepairError in
// tools_builtin_write_edit.go: that legacy string is asserted byte-for-byte by
// multi_edit_repair_test.go and is left untouched until a caller opts in.
func multiEditBatchDiagnostic(path string, failedIdx, total, applied int, content, sample string, atomic bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "multi_edit: repair failed at edit %d of %d after %d edit(s) attempted in %s", failedIdx, total, total, path)
	switch {
	case applied == 0:
		b.WriteString("; no edit matched; re-send the whole batch with the failing edit corrected")
	case atomic:
		fmt.Fprintf(&b, "; %d edit(s) matched cleanly in memory but no bytes were written (atomic batch); re-send the whole batch with the failing edit corrected", applied)
	default:
		fmt.Fprintf(&b, "; %d edit(s) were applied and written; do not re-send them; re-send only the failing edit(s) with the correction", applied)
	}
	if s := formatSimilarLines(content, sample); s != "" {
		b.WriteString("; ")
		b.WriteString(s)
	}
	return b.String()
}
