package memory

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildBudgetChain writes one AGENTS.md per directory along a chain rooted at
// base. sizes are nearest-first: sizes[0] lives in the deepest directory,
// sizes[len-1] in base. It returns the deepest directory (the "cwd").
func buildBudgetChain(t *testing.T, base string, sizes []int) string {
	t.Helper()
	current := base
	for i, n := range sizes {
		if err := os.MkdirAll(current, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(current, "AGENTS.md"), []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		if i < len(sizes)-1 {
			current = filepath.Join(current, "d"+strconv.Itoa(i))
		}
	}
	return current
}

func sumTextBytes(texts ...string) int {
	total := 0
	for _, text := range texts {
		total += len(text)
	}
	return total
}

// budgetMarkerCases places the walk's remaining budget in the narrow window
// [DefaultLimit, DefaultLimit+len(TruncationMarker)) before a file whose body is
// exactly DefaultLimit bytes and which was clipped by the per-file cap. Such a
// document gets a TruncationMarker appended; the marker must be reserved from
// the budget, otherwise the combined text silently exceeds totalBudget. The
// last case is a control where the marker still fits.
//
// The chain built by budgetMarkerChain is (nearest first): three over-cap docs
// (each DefaultLimit+marker bytes), one small untrimmed "filler" doc, then a
// final over-cap doc. The filler size is chosen so that, just before the final
// doc is charged, the remaining budget equals tc.remaining.
func budgetMarkerCases() []struct {
	name      string
	remaining int
} {
	marker := len(TruncationMarker)
	return []struct {
		name      string
		remaining int
	}{
		{"remaining equals per-file cap", DefaultLimit},
		{"remaining inside marker window", DefaultLimit + 10},
		{"remaining one byte under marker window", DefaultLimit + marker - 1},
		{"remaining at marker window edge", DefaultLimit + marker}, // control: fits exactly
	}
}

// budgetMarkerChain builds a five-directory chain whose nearest-first file
// sizes drive a capped document into the marker window described above.
func budgetMarkerChain(t *testing.T, base string, remaining int) string {
	t.Helper()
	over := DefaultLimit + 1000 // any file above the cap yields a DefaultLimit-byte body
	// Three over-cap docs cost 3*(DefaultLimit+marker); the filler then brings
	// the used total to totalBudget-remaining.
	filler := totalBudget - 3*(DefaultLimit+len(TruncationMarker)) - remaining
	if filler <= 0 || filler > DefaultLimit {
		t.Fatalf("filler = %d out of range for remaining = %d", filler, remaining)
	}
	return buildBudgetChain(t, base, []int{over, over, over, filler, over})
}

func docTextLengths(docs []Doc) []string {
	texts := make([]string, len(docs))
	for i, doc := range docs {
		texts[i] = doc.Text
	}
	return texts
}

func TestLoadInTotalBudgetAccountsForTruncationMarker(t *testing.T) {
	for _, tc := range budgetMarkerCases() {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			cwd := budgetMarkerChain(t, base, tc.remaining)

			docs, err := LoadIn(cwd, base, DefaultLimit)
			if err != nil {
				t.Fatalf("LoadIn: %v", err)
			}
			if len(docs) != 5 {
				t.Fatalf("docs = %d, want 5: %#v", len(docs), docs)
			}
			if !docs[4].Truncated {
				t.Error("outermost (capped) document must be marked truncated")
			}
			if total := sumTextBytes(docTextLengths(docs)...); total > totalBudget {
				t.Errorf("combined text = %d bytes, budget = %d (remaining was %d)", total, totalBudget, tc.remaining)
			}
		})
	}
}

func TestLoadForDirsTotalBudgetAccountsForTruncationMarker(t *testing.T) {
	for _, tc := range budgetMarkerCases() {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			cwd := budgetMarkerChain(t, base, tc.remaining)
			rel, err := filepath.Rel(base, cwd)
			if err != nil {
				t.Fatal(err)
			}

			docs := LoadForDirs(base, []string{rel})
			if len(docs) != 5 {
				t.Fatalf("docs = %d, want 5: %#v", len(docs), docs)
			}
			if !docs[4].Truncated {
				t.Error("outermost (capped) document must be marked truncated")
			}
			if total := sumTextBytes(docTextLengths(docs)...); total > totalBudget {
				t.Errorf("combined text = %d bytes, budget = %d (remaining was %d)", total, totalBudget, tc.remaining)
			}
		})
	}
}

func TestLoadWithBoundaryTotalBudgetAccountsForTruncationMarker(t *testing.T) {
	for _, tc := range budgetMarkerCases() {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			cwd := budgetMarkerChain(t, base, tc.remaining)
			if err := os.MkdirAll(filepath.Join(base, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}

			insts, err := LoadWithBoundary(cwd, true, LoadOptions{Home: t.TempDir()})
			if err != nil {
				t.Fatalf("LoadWithBoundary: %v", err)
			}
			if len(insts) != 5 {
				t.Fatalf("insts = %d, want 5: %#v", len(insts), insts)
			}
			if !insts[0].Truncated {
				t.Error("outermost (capped) instruction must be marked truncated")
			}
			texts := make([]string, len(insts))
			for i, inst := range insts {
				texts[i] = inst.Text
			}
			if total := sumTextBytes(texts...); total > totalBudget {
				t.Errorf("combined text = %d bytes, budget = %d (remaining was %d)", total, totalBudget, tc.remaining)
			}
		})
	}
}
