// Package memory — project-root boundary, override files and trust gate.
//
// This file adds an opt-in instruction loader that follows Codex's
// instruction-discovery refinement: walk up to the nearest project-root marker,
// collect AGENTS.md from the root down to the working directory (root first),
// prefer AGENTS.override.md over AGENTS.md in each directory, and skip ALL
// project documents for an untrusted project. Existing Load/LoadIn behaviour is
// untouched.
//
// The per-file and total caps, rune-safe clipping and truncation marker are not
// reimplemented here: this file calls readInstructionFile and clipRunes from
// memory.go directly (same package) and reuses DefaultLimit, totalBudget and
// TruncationMarker.
package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultMarkers are the project-root markers recognised by ProjectRoot when
// the caller does not supply its own list. ".git" covers both a directory and
// a worktree/submodule pointer file; ".argent" covers agent-native projects.
var DefaultMarkers = []string{".git", ".argent"}

// LoadOptions configures LoadWithBoundary. Zero values are valid: they select
// the default markers, the current user's home directory and DefaultLimit.
type LoadOptions struct {
	// Markers are the project-root markers passed to ProjectRoot. An empty
	// slice selects DefaultMarkers.
	Markers []string
	// Home is the outer walk boundary and the source of user-level documents.
	// An empty string means os.UserHomeDir(). Tests set it to a scratch dir.
	Home string
	// Limit is the per-file byte cap. Values <= 0 select DefaultLimit; the
	// 32 KiB total budget always applies.
	Limit int
}

// Instruction is one instruction document together with its source path, so
// the rendered system prompt can show provenance. It is intentionally a
// separate type from Doc to keep the opt-in loader's contract explicit.
type Instruction struct {
	// Source is the absolute path of the file this instruction came from.
	Source string
	// Text is the file content, possibly clipped at the size limits.
	Text string
	// Truncated reports whether Text was cut by either the per-file cap or the
	// combined total budget.
	Truncated bool
}

// ProjectRoot walks up from dir and returns the nearest ancestor directory
// that contains any project-root marker. It recognises both a ".git"
// directory and a ".git" file (worktree/submodule pointer): only the entry's
// existence matters. The walk stops at the filesystem root or at home, so a
// marker above the user's home directory is never selected. When markers is
// empty, DefaultMarkers are used. It returns "" when no marker is found.
func ProjectRoot(dir string, markers ...string) string {
	home, _ := os.UserHomeDir()
	return projectRoot(dir, home, markers)
}

// projectRoot is the testable core of ProjectRoot: home is supplied explicitly
// so tests can control the upper bound without touching the real home dir.
func projectRoot(dir, home string, markers []string) string {
	if len(markers) == 0 {
		markers = DefaultMarkers
	}
	current, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	current = filepath.Clean(current)

	stop := ""
	if home != "" {
		if abs, err := filepath.Abs(home); err == nil {
			stop = filepath.Clean(abs)
		}
	}

	for depth := 0; depth < maxDepth; depth++ {
		for _, marker := range markers {
			if marker == "" {
				continue
			}
			if _, err := os.Lstat(filepath.Join(current, marker)); err == nil {
				return current
			}
		}
		if samePath(current, stop) {
			return ""
		}
		parent := filepath.Dir(current)
		if parent == current { // filesystem root
			return ""
		}
		current = parent
	}
	return ""
}

// LoadWithBoundary is the opt-in loader with a project-root boundary and a
// trust gate.
//
// When trust is false, NO project documents are collected: only user-level
// documents from opts.Home (os.UserHomeDir when empty) are returned.
//
// When trust is true, the project root is found with ProjectRoot and
// instruction files are collected from that root down to dir inclusive, in
// root-to-cwd order. In each directory AGENTS.override.md wins over AGENTS.md
// (exactly one of them is used), followed by CLAUDE.md. If no project root is
// found, dir itself is used as the single scanned directory.
//
// Files are read and charged against the shared total budget in cwd-to-root
// (nearest-first) order, so when the budget runs out the outermost, most
// general instructions are the ones dropped and the nearest, most specific
// ones survive. This matches the "nearer wins" contract of Block/LoadIn. The
// returned slice is still assembled root-to-cwd for callers.
//
// The same DefaultLimit per-file cap, 32 KiB total budget, rune-safe clipping
// and TruncationMarker as Load/LoadIn apply. Each Instruction carries its
// Source path. Existing Load/LoadIn behaviour is unchanged.
func LoadWithBoundary(dir string, trust bool, opts LoadOptions) ([]Instruction, error) {
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	absDir = filepath.Clean(absDir)

	var dirs []string
	if !trust {
		// Untrusted: never read project files; only user-level docs.
		if home != "" {
			dirs = []string{filepath.Clean(home)}
		}
	} else if root := projectRoot(absDir, home, opts.Markers); root != "" {
		dirs = dirsFromRootToDir(root, absDir)
	} else {
		// No project root: scan the working directory only.
		dirs = []string{absDir}
	}

	// dirs is root-to-cwd. Read it in reverse (cwd-to-root) so the shared 32 KiB
	// budget is spent on the most specific instructions first: when it runs out
	// the outermost, most general files are the ones dropped. Collect one group
	// per directory so the result can still be assembled in the documented
	// root-to-cwd order, with the within-directory AGENTS/CLAUDE order intact.
	var (
		perDir [][]Instruction
		used   int
	)
	for i := len(dirs) - 1; i >= 0; i-- {
		var group []Instruction
		for _, path := range dirPaths(dirs[i]) {
			var stop bool
			group, stop = appendInstruction(group, path, limit, &used)
			if stop {
				perDir = append(perDir, group)
				return assembleRootFirst(perDir), nil
			}
		}
		perDir = append(perDir, group)
	}
	return assembleRootFirst(perDir), nil
}

// assembleRootFirst joins per-directory groups that were collected in
// cwd-to-root order back into the documented root-to-cwd return order, keeping
// the instructions inside each group (AGENTS slot before CLAUDE.md) in place.
func assembleRootFirst(perDir [][]Instruction) []Instruction {
	var out []Instruction
	for i := len(perDir) - 1; i >= 0; i-- {
		out = append(out, perDir[i]...)
	}
	return out
}

// dirsFromRootToDir returns the directory chain from root down to dir,
// inclusive, in root-first order. It walks up from dir and stops as soon as
// root is reached (or the filesystem root, as a safety net).
func dirsFromRootToDir(root, dir string) []string {
	var rev []string
	current := dir
	for depth := 0; depth < maxDepth; depth++ {
		rev = append(rev, current)
		if samePath(current, root) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	dirs := make([]string, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		dirs = append(dirs, rev[i])
	}
	return dirs
}

// dirPaths returns the instruction files to read in one directory, in order.
// AGENTS.override.md takes the place of AGENTS.md when it exists; CLAUDE.md is
// always considered after the AGENTS slot.
func dirPaths(dir string) []string {
	override := filepath.Join(dir, "AGENTS.override.md")
	agents := filepath.Join(dir, "AGENTS.md")
	agentsPath := agents
	if info, err := os.Stat(override); err == nil && !info.IsDir() {
		agentsPath = override
	}
	return []string{agentsPath, filepath.Join(dir, "CLAUDE.md")}
}

// appendInstruction reads path with the shared per-file cap and appends it to
// out while enforcing the shared total budget. It returns the updated slice
// and a stop flag meaning "budget exhausted, caller should stop walking".
// All clipping/truncation work is delegated to memory.go helpers.
func appendInstruction(out []Instruction, path string, limit int, used *int) ([]Instruction, bool) {
	body, fileTruncated, err := readInstructionFile(path, limit)
	if err != nil || body == "" {
		return out, false
	}

	budgetTruncated := false
	if limit > 0 {
		remaining := totalBudget - *used
		if remaining <= 0 {
			return out, true
		}
		if len(body) > remaining {
			room := remaining - len(TruncationMarker)
			if room <= 0 {
				return out, true
			}
			body = clipRunes(body, room)
			budgetTruncated = true
		}
	}

	inst := Instruction{Source: path, Text: body, Truncated: fileTruncated || budgetTruncated}
	if inst.Truncated {
		inst.Text = strings.TrimRight(inst.Text, " \t\r\n") + TruncationMarker
	}
	out = append(out, inst)
	*used += len(inst.Text)
	return out, budgetTruncated || *used >= totalBudget
}

// InstructionsToDocs converts opt-in loader results to the Doc type accepted
// by Block, so the app can render them with the existing system-prompt block.
func InstructionsToDocs(insts []Instruction) []Doc {
	if len(insts) == 0 {
		return nil
	}
	docs := make([]Doc, 0, len(insts))
	for _, in := range insts {
		docs = append(docs, Doc{Path: in.Source, Text: in.Text, Truncated: in.Truncated})
	}
	return docs
}
