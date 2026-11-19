package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ---- tool_search ----

const toolSearchMaxResults = 20

// toolSearchTool reads the registry's own specs, so it needs no dependency on
// the agent package that consumes those specs.
type toolSearchTool struct {
	registry *Registry
}

func (toolSearchTool) Name() string { return "tool_search" }
func (toolSearchTool) Description() string {
	return "Find up to 20 tool names whose name or description matches a substring."
}
func (toolSearchTool) Mutating() bool { return false }
func (toolSearchTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"case-insensitive substring of a tool name or description"},"limit":{"type":"integer","description":"maximum matches, capped at 20"}},"required":["query"]}`
}

func (t toolSearchTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("tool_search: bad arguments: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(args.Query))
	if query == "" {
		return "", fmt.Errorf("tool_search: query must not be empty")
	}
	if t.registry == nil {
		return "(no tools registered)", nil
	}

	limit := args.Limit
	if limit <= 0 || limit > toolSearchMaxResults {
		limit = toolSearchMaxResults
	}
	var matches []string
	for _, spec := range t.registry.Specs() {
		name := spec.Function.Name
		description := spec.Function.Description
		if !strings.Contains(strings.ToLower(name), query) && !strings.Contains(strings.ToLower(description), query) {
			continue
		}
		oneLine := strings.TrimSpace(strings.SplitN(description, "\n", 2)[0])
		if oneLine == "" {
			oneLine = "(no description)"
		}
		matches = append(matches, name+": "+oneLine)
		if len(matches) >= limit {
			break
		}
	}
	if len(matches) == 0 {
		return fmt.Sprintf("no tools match %q", args.Query), nil
	}
	return clip(ctx, strings.Join(matches, "\n")), nil
}
