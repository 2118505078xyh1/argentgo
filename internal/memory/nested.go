package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// candidate is one discovered instruction file with its rank inputs.
// distance is the number of directory levels between the file and the active
// directory whose walk found it; seq preserves discovery order for ties.
type candidate struct {
	path     string
	distance int
	seq      int
}

// LoadForDirs collects instruction files for a set of active subdirectories
// and their ancestors, stopping at the workspace root. It is meant for
// "nested AGENTS.md" discovery: when work touches src/parser and src/lexer,
// the caller passes those directories so the specific rules of each subtree
// (and the shared rules above them) are included in the system prompt.
//
// The input dirs are resolved against cwd. For each dir, every ancestor up to
// and including the workspace root is considered, in nearest-first order: a
// file in a deeper directory outranks the same-named file nearer the root.
// Both FileNames are read in each directory. Paths are deduplicated by their
// resolved (absolute, cleaned, case-folded on Windows) location so a file
// shared by several subtrees is injected only once, at its most specific rank.
//
// Existing Load behaviour is unchanged; LoadForDirs is an additive API. The
// same per-file limit and total byte budget used by LoadIn apply here, and the
// returned docs are already clipped and ranked for Block(). A directory that
// cannot be resolved or read is skipped rather than failing the whole call.
func LoadForDirs(cwd string, dirs []string) []Doc {
	workspace, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	workspace = filepath.Clean(workspace)
	if info, statErr := os.Stat(workspace); statErr != nil || !info.IsDir() {
		return nil
	}

	// Rank lists for each distinct ancestor chain. Shorter distance to the
	// active directory means more specific; ties keep the first-seen order.
	seen := make(map[string]candidate)
	seq := 0
	normalized := make(map[string]bool)

	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		abs := dir
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workspace, abs)
		}
		abs, err = filepath.Abs(abs)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if !withinWorkspace(abs, workspace) {
			// Never walk outside the workspace root.
			continue
		}
		key := normalizePath(abs)
		if normalized[key] {
			continue
		}
		normalized[key] = true

		distance := 0
		current := abs
		for {
			seq++
			for _, name := range FileNames {
				path := filepath.Join(current, name)
				pKey := normalizePath(path)
				if old, exists := seen[pKey]; !exists || distance < old.distance {
					seen[pKey] = candidate{path: path, distance: distance, seq: seq}
				}
			}
			if samePath(current, workspace) {
				break
			}
			parent := filepath.Dir(current)
			if parent == current || !withinWorkspace(parent, workspace) {
				break
			}
			current = parent
			distance++
		}
	}

	ranked := make([]candidate, 0, len(seen))
	for _, c := range seen {
		ranked = append(ranked, c)
	}
	sortCandidates(ranked)

	var docs []Doc
	used := 0
	for _, c := range ranked {
		info, err := os.Stat(c.path)
		if err != nil || info.IsDir() {
			continue
		}
		body, fileTruncated, err := readInstructionFile(c.path, DefaultLimit)
		if err != nil || body == "" {
			continue
		}

		// Reserve the truncation marker's bytes when the per-file cap already
		// clipped this body; see the matching logic in LoadIn.
		markerCost := 0
		if fileTruncated {
			markerCost = len(TruncationMarker)
		}
		remaining := totalBudget - used
		if remaining <= markerCost {
			break
		}
		budgetTruncated := false
		if len(body)+markerCost > remaining {
			room := remaining - len(TruncationMarker)
			if room <= 0 {
				break
			}
			body = clipRunes(body, room)
			budgetTruncated = true
		}

		doc := Doc{Path: c.path, Text: body, Truncated: fileTruncated || budgetTruncated}
		if doc.Truncated {
			doc.Text = strings.TrimRight(doc.Text, " \t\r\n") + TruncationMarker
		}
		docs = append(docs, doc)
		used += len(doc.Text)
		if budgetTruncated || used >= totalBudget {
			break
		}
	}
	return docs
}

// withinWorkspace reports whether path is the workspace root or lies below it.
// The comparison is done on cleaned absolute paths and is case-insensitive on
// Windows, matching samePath used by LoadIn.
func withinWorkspace(path, workspace string) bool {
	if samePath(path, workspace) {
		return true
	}
	prefix := workspace + string(filepath.Separator)
	if filepath.Separator == '\\' {
		return strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix))
	}
	return strings.HasPrefix(path, prefix)
}

// normalizePath returns a canonical key for deduplication. Windows paths are
// case-insensitive, so fold them; other platforms keep their exact case.
func normalizePath(path string) string {
	path = filepath.Clean(path)
	if filepath.Separator == '\\' {
		return strings.ToLower(path)
	}
	return path
}

// samePath compares two cleaned paths, case-insensitively on Windows.
func samePath(a, b string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// sortCandidates orders docs most-specific first: smaller distance (closer to
// an active directory) wins, then earlier discovery order breaks ties, then
// path text for a stable result set.
func sortCandidates(items []candidate) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j], items[j-1]
			less := a.distance < b.distance ||
				(a.distance == b.distance && a.seq < b.seq) ||
				(a.distance == b.distance && a.seq == b.seq && a.path < b.path)
			if !less {
				break
			}
			items[j-1], items[j] = a, b
		}
	}
}
