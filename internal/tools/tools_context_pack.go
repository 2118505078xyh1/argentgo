package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// ---- context_pack ----
//
// context_pack is the multi-file counterpart of read's gap-centred window: it
// packs real file contents around an edit anchor under a byte budget so the
// model can skip a "read whole file" round trip. It is read-only, bounded, and
// never splits a UTF-8 rune.

const (
	contextPackDefaultBudget = 8000
	contextPackMaxBudget     = 32000
	// A smaller budget cannot even hold the truncation marker, so tiny values
	// are clamped up rather than emitting an over-budget marker.
	contextPackMinBudget = 64
	// Per-side line window cap, matching readTool's readWindowCap.
	contextPackWindowCap = 200
	// Auto-selected neighbours are bounded so one call cannot walk a huge tree.
	contextPackMaxFiles = 24
)

type contextPackTool struct{}

func (contextPackTool) Name() string { return "context_pack" }

func (contextPackTool) Description() string {
	return "Pack real file contents around an edit anchor under a byte budget; " +
		"the target file is always emitted last (nearest-last). " +
		"files overrides the automatic selection of sibling files plus repo entry points. " +
		"For the target, set line with before/after (each capped at 200) to pack only a " +
		"gap-centred window instead of the whole file. " +
		"budget_bytes defaults to 8000 and is capped at 32000; packing stops at a whole " +
		"block boundary and reports [truncated at N bytes] when the budget is hit."
}

func (contextPackTool) Mutating() bool { return false }

func (contextPackTool) Schema() string {
	return `{"type":"object","properties":{"target":{"type":"string","description":"workspace-relative edit anchor file; always packed last"},"files":{"type":"array","items":{"type":"string"},"description":"explicit workspace-relative files to pack; overrides automatic selection"},"budget_bytes":{"type":"integer","description":"total output byte budget (default 8000, cap 32000)"},"line":{"type":"integer","description":"1-based anchor line; when > 0 only the gap window of the target is emitted"},"before":{"type":"integer","description":"lines above the anchor (0-200, capped)"},"after":{"type":"integer","description":"lines below the anchor (0-200, capped)"}},"required":["target"]}`
}

// registerContextPack installs the tool before the memory_* tail so that tail
// stays adjacent to the trailing tool_search. It is exported so a caller that
// owns a Registry but cannot edit tools_registry.go can install the tool too;
// the default New/NewWithLimit path already calls it. Repeat calls are no-ops.
func registerContextPack(r *Registry) {
	if r == nil {
		return
	}
	if _, exists := r.tools[contextPackTool{}.Name()]; exists {
		return
	}
	tool := contextPackTool{}
	r.tools[tool.Name()] = tool
	idx := len(r.order)
	for i, name := range r.order {
		if strings.HasPrefix(name, "memory_") || name == "tool_search" {
			idx = i
			break
		}
	}
	r.order = append(r.order, "")
	copy(r.order[idx+1:], r.order[idx:])
	r.order[idx] = tool.Name()
}

type contextPackArgs struct {
	Target      string   `json:"target"`
	Files       []string `json:"files"`
	BudgetBytes int      `json:"budget_bytes"`
	Line        int      `json:"line"`
	Before      int      `json:"before"`
	After       int      `json:"after"`
}

func (contextPackTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args contextPackArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("context_pack: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.Target) == "" {
		return "", fmt.Errorf("context_pack: target is required")
	}

	targetAbs, err := resolve(root, args.Target)
	if err != nil {
		return "", err
	}
	targetRel, err := contextPackRel(root, targetAbs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(targetAbs)
	if err != nil {
		return "", fmt.Errorf("context_pack: target %q: %w", args.Target, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("context_pack: target %q is not a regular file", args.Target)
	}

	targetContent, err := contextPackRead(targetAbs)
	if err != nil {
		return "", fmt.Errorf("context_pack: target %q: %w", args.Target, err)
	}
	if args.Line > 0 {
		targetContent = contextPackWindow(targetContent, args.Line, args.Before, args.After)
	}

	others, err := contextPackOthers(root, args.Files, targetRel)
	if err != nil {
		return "", err
	}
	blocks := make([]string, 0, len(others)+1)
	for _, rel := range others {
		abs, err := resolve(root, rel)
		if err != nil {
			return "", err
		}
		content, err := contextPackRead(abs)
		if err != nil {
			// A missing or unreadable neighbour must not kill the whole pack;
			// explicit path escapes were already rejected by resolve above.
			continue
		}
		blocks = append(blocks, fmt.Sprintf("## %s ##\n%s", rel, content))
	}
	// Nearest-last: the anchor is the final block the model reads.
	blocks = append(blocks, fmt.Sprintf("## %s ##\n%s", targetRel, targetContent))

	out, _ := contextPackAssemble(blocks, contextPackBudget(args.BudgetBytes))
	return out, nil
}

// contextPackOthers selects the non-target files. Explicit files win; otherwise
// take the target's siblings plus repo entry points. The result is deduplicated
// and ordered so same-directory (nearest) files come last, just before target.
func contextPackOthers(root string, explicit []string, targetRel string) ([]string, error) {
	var rels []string
	if len(explicit) > 0 {
		for _, raw := range explicit {
			abs, err := resolve(root, raw)
			if err != nil {
				return nil, err
			}
			rel, err := contextPackRel(root, abs)
			if err != nil {
				return nil, err
			}
			rels = append(rels, rel)
		}
	} else {
		var err error
		rels, err = contextPackAuto(root, targetRel)
		if err != nil {
			return nil, err
		}
	}

	seen := make(map[string]bool, len(rels))
	uniq := make([]string, 0, len(rels))
	for _, rel := range rels {
		if rel == "" || rel == targetRel || seen[rel] {
			continue
		}
		seen[rel] = true
		uniq = append(uniq, rel)
	}
	targetDir := filepath.ToSlash(filepath.Dir(targetRel))
	sort.Slice(uniq, func(i, j int) bool {
		ri, rj := contextPackRank(uniq[i], targetDir), contextPackRank(uniq[j], targetDir)
		if ri != rj {
			return ri < rj
		}
		return uniq[i] < uniq[j]
	})
	if len(uniq) > contextPackMaxFiles {
		// Nearest files sort last, so keep the tail.
		uniq = uniq[len(uniq)-contextPackMaxFiles:]
	}
	return uniq, nil
}

func contextPackRank(rel, targetDir string) int {
	if filepath.ToSlash(filepath.Dir(rel)) == targetDir {
		return 1
	}
	return 0
}

// contextPackAuto picks files next to the target plus entry points at the
// workspace root. Only immediate children are considered, symlinks are skipped,
// and the repo_map ignore list keeps build/CI noise out of the pack.
func contextPackAuto(root, targetRel string) ([]string, error) {
	rootAbs, err := resolve(root, ".")
	if err != nil {
		return nil, err
	}
	targetAbs, err := resolve(root, targetRel)
	if err != nil {
		return nil, err
	}
	var rels []string
	seen := make(map[string]bool)
	addDir := func(dir string, entryPointsOnly bool) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || !entry.Type().IsRegular() || repoMapIgnoredDirs[entry.Name()] {
				continue
			}
			if entryPointsOnly && !repoMapEntryPoints[strings.ToLower(entry.Name())] {
				continue
			}
			abs := filepath.Join(dir, entry.Name())
			rel, err := contextPackRel(rootAbs, abs)
			if err != nil || rel == targetRel || seen[rel] {
				continue
			}
			seen[rel] = true
			rels = append(rels, rel)
		}
	}
	addDir(filepath.Dir(targetAbs), false)
	addDir(rootAbs, true)
	return rels, nil
}

// contextPackWindow mirrors readTool's gap-centred window: before/after are
// independent per-side budgets clamped to [0, 200], and the trailing newline
// sentinel is not counted as a real line.
func contextPackWindow(content string, line, before, after int) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(content, "\n")
	total := len(lines)
	if strings.HasSuffix(content, "\n") {
		total--
	}
	clamp := func(v int) int {
		if v < 0 {
			return 0
		}
		if v > contextPackWindowCap {
			return contextPackWindowCap
		}
		return v
	}
	start := line - clamp(before)
	if start < 1 {
		start = 1
	}
	end := line + clamp(after)
	if end > total {
		end = total
	}
	if start > total || end < start {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

func contextPackBudget(v int) int {
	if v <= 0 {
		return contextPackDefaultBudget
	}
	if v > contextPackMaxBudget {
		return contextPackMaxBudget
	}
	if v < contextPackMinBudget {
		return contextPackMinBudget
	}
	return v
}

// contextPackAssemble joins whole blocks until the next one would exceed the
// byte budget. Truncation therefore happens at a block boundary: no block and
// no UTF-8 rune is ever cut in half. The marker's own bytes are reserved, so
// the returned string never exceeds budget.
func contextPackAssemble(blocks []string, budget int) (string, bool) {
	included := make([]string, 0, len(blocks))
	used := 0
	truncated := false
	for _, block := range blocks {
		seg := block
		if len(included) > 0 {
			seg = "\n" + block
		}
		if used+len(seg) > budget {
			truncated = true
			break
		}
		included = append(included, block)
		used += len(seg)
	}
	if !truncated {
		return strings.Join(included, "\n"), false
	}
	marker := func(n int) string { return fmt.Sprintf("\n[truncated at %d bytes]", n) }
	m := marker(used)
	for used+len(m) > budget && len(included) > 0 {
		last := included[len(included)-1]
		included = included[:len(included)-1]
		used -= len(last)
		if len(included) > 0 {
			used-- // the separator before the removed block
		}
		m = marker(used)
	}
	return strings.Join(included, "\n") + m, true
}

func contextPackRel(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %q is outside the workspace", abs)
	}
	return rel, nil
}

func contextPackRead(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not UTF-8 text", path)
	}
	return string(data), nil
}
