package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ---- stdlib-portable productivity tools ----
//
// diff compares two workspace files with a bounded, prefix/suffix hunk. usage
// and history report run/session data through consumer-side interfaces declared
// here, so internal/tools stays independent of internal/agent (runState) and
// internal/session. The app wires the real sources with SetUsageSource and
// SetHistorySource; until then those tools report that data is unavailable.

// ---- diff ----

type diffTool struct{}

var _ Tool = (*diffTool)(nil)

func (diffTool) Name() string { return "diff" }

func (diffTool) Description() string {
	return "Compare two workspace files line by line: added/removed/changed counts plus a bounded unified-style hunk. Read-only."
}

func (diffTool) Mutating() bool { return false }

func (diffTool) Schema() string {
	return `{"type":"object","properties":{"path_a":{"type":"string","description":"first workspace file (relative path)"},"path_b":{"type":"string","description":"second workspace file (relative path)"}},"required":["path_a","path_b"]}`
}

func (diffTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		PathA string `json:"path_a"`
		PathB string `json:"path_b"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("diff: bad arguments: %w", err)
	}
	if strings.TrimSpace(args.PathA) == "" || strings.TrimSpace(args.PathB) == "" {
		return "", fmt.Errorf("diff: both path_a and path_b are required")
	}
	absA, err := resolve(root, args.PathA)
	if err != nil {
		return "", fmt.Errorf("diff: path_a: %w", err)
	}
	absB, err := resolve(root, args.PathB)
	if err != nil {
		return "", fmt.Errorf("diff: path_b: %w", err)
	}
	before, err := os.ReadFile(absA)
	if err != nil {
		return "", fmt.Errorf("diff: read path_a: %w", err)
	}
	after, err := os.ReadFile(absB)
	if err != nil {
		return "", fmt.Errorf("diff: read path_b: %w", err)
	}
	return clip(ctx, renderFileDiff(args.PathA, args.PathB, before, after)), nil
}

const (
	diffContextLines = 3  // identical lines shown on either side of a hunk
	diffHunkLines    = 20 // maximum paired changed lines shown before collapsing
)

// renderFileDiff trims the common prefix and suffix, then renders the changed
// middle as a compact unified-style hunk. Counts are derived from that same
// middle region so the numbers always match the lines shown. CRLF is
// normalized by splitDiffLines so a line-ending change alone is not a diff.
func renderFileDiff(pathA, pathB string, before, after []byte) string {
	a := splitDiffLines(before)
	b := splitDiffLines(after)

	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix &&
		a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	midA := a[prefix : len(a)-suffix]
	midB := b[prefix : len(b)-suffix]

	changed := min(len(midA), len(midB))
	removed := len(midA) - changed
	added := len(midB) - changed

	var sb strings.Builder
	fmt.Fprintf(&sb, "diff %s -> %s\n", pathA, pathB)
	fmt.Fprintf(&sb, "+%d added  -%d removed  ~%d changed\n", added, removed, changed)
	if added == 0 && removed == 0 && changed == 0 {
		fmt.Fprintf(&sb, "no differences (%d lines identical)\n", len(a))
		return sb.String()
	}
	fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", prefix+1, len(midA), prefix+1, len(midB))

	ctxStart := prefix - diffContextLines
	if ctxStart < 0 {
		ctxStart = 0
	}
	for _, line := range a[ctxStart:prefix] {
		sb.WriteString(" " + line + "\n")
	}

	span := max(len(midA), len(midB))
	shown := min(span, diffHunkLines)
	for i := 0; i < shown; i++ {
		if i < len(midA) {
			sb.WriteString("-" + midA[i] + "\n")
		}
		if i < len(midB) {
			sb.WriteString("+" + midB[i] + "\n")
		}
	}
	if shown < span {
		fmt.Fprintf(&sb, "... %d more changed lines ...\n", span-shown)
	}

	ctxAfterStart := len(a) - suffix
	ctxAfterEnd := min(ctxAfterStart+diffContextLines, len(a))
	for _, line := range a[ctxAfterStart:ctxAfterEnd] {
		sb.WriteString(" " + line + "\n")
	}
	return sb.String()
}

// ---- usage ----

const usageUnavailable = "usage data unavailable"

// UsageSource supplies the current run's token/cost usage as a JSON string.
// Declared on the consumer side so internal/tools does not import internal/agent
// and its runState. App wiring: implement UsageJSON on an adapter over the
// runState (e.g. its accumulated llm.Usage / spent USD) and call
// Registry.SetUsageSource(adapter) once the run state exists.
type UsageSource interface {
	UsageJSON() string
}

type usageTool struct {
	source UsageSource
}

var _ Tool = (*usageTool)(nil)

func (*usageTool) Name() string { return "usage" }

func (*usageTool) Description() string {
	return "Report the current run's accumulated token and cost usage as JSON. Read-only."
}

func (*usageTool) Mutating() bool { return false }

func (*usageTool) Schema() string { return `{"type":"object","properties":{}}` }

func (t *usageTool) Run(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	if t.source == nil {
		return usageUnavailable, nil
	}
	return clip(ctx, t.source.UsageJSON()), nil
}

// SetUsageSource installs the source behind the usage tool. Following the
// SetCheckpointSource pattern the tool is rebuilt in place, so registration
// order and advertised specs stay stable. A nil source makes the tool report
// that usage data is unavailable instead of failing the run.
func (r *Registry) SetUsageSource(src UsageSource) {
	t := &usageTool{source: src}
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// ---- history ----

const historyUnavailable = "history unavailable"

// HistorySource searches the current session's JSONL records. Declared on the
// consumer side so internal/tools does not import internal/session. App wiring:
// implement Search on an adapter over the active *session.Session and call
// Registry.SetHistorySource(adapter) once the session is known. The adapter
// decides how an empty query and a non-positive or oversized limit behave.
type HistorySource interface {
	Search(query string, limit int) string
}

type historyTool struct {
	source HistorySource
}

var _ Tool = (*historyTool)(nil)

func (*historyTool) Name() string { return "history" }

func (*historyTool) Description() string {
	return "Search the current session's records for a substring in messages and tool calls. Read-only."
}

func (*historyTool) Mutating() bool { return false }

func (*historyTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"substring to match; empty lists the most recent records"},"limit":{"type":"integer","description":"maximum matches to return; default is left to the source"}}}`
}

func (t *historyTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("history: bad arguments: %w", err)
	}
	if t.source == nil {
		return historyUnavailable, nil
	}
	return clip(ctx, t.source.Search(args.Query, args.Limit)), nil
}

// SetHistorySource installs the source behind the history tool. The tool is
// rebuilt in place so registration order stays stable; a nil source makes the
// tool report that history is unavailable instead of failing the run.
func (r *Registry) SetHistorySource(src HistorySource) {
	t := &historyTool{source: src}
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}
