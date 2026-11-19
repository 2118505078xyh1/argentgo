package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
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
	return "Find up to 20 tools ranked by name and description relevance."
}
func (toolSearchTool) Mutating() bool { return false }
func (toolSearchTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"tool name or description words, case-insensitive"},"limit":{"type":"integer","description":"maximum matches, capped at 20"}},"required":["query"]}`
}

type toolMatch struct {
	name  string
	line  string
	score int
	order int
}

func (t toolSearchTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("tool_search: bad arguments: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	tokens := searchTokens(query)
	if len(tokens) == 0 {
		return "", fmt.Errorf("tool_search: query must not be empty")
	}
	if t.registry == nil {
		return "(no tools registered)", nil
	}

	limit := args.Limit
	if limit <= 0 || limit > toolSearchMaxResults {
		limit = toolSearchMaxResults
	}
	queryLower := strings.ToLower(query)
	var matches []toolMatch
	for order, spec := range t.registry.Specs() {
		name := spec.Function.Name
		description := spec.Function.Description
		score, ok := toolMatchScore(name, description, tokens, queryLower)
		if !ok {
			continue
		}
		oneLine := strings.TrimSpace(strings.SplitN(description, "\n", 2)[0])
		if oneLine == "" {
			oneLine = "(no description)"
		}
		matches = append(matches, toolMatch{name: name, line: oneLine, score: score, order: order})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].order < matches[j].order
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	if len(matches) == 0 {
		return fmt.Sprintf("no tools match %q", args.Query), nil
	}
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		lines = append(lines, match.name+": "+match.line)
	}
	return clip(ctx, strings.Join(lines, "\n")), nil
}

// searchTokens splits a query into lowercase words. Underscores are kept so a
// tool name such as web_fetch stays one token; punctuation is ignored.
func searchTokens(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// toolMatchScore scores one spec. Every token must match somewhere: a query
// with several words should not return tools that match only one of them.
func toolMatchScore(name, description string, tokens []string, queryLower string) (int, bool) {
	lowerName := strings.ToLower(name)
	lowerDescription := strings.ToLower(description)
	words := strings.FieldsFunc(lowerDescription, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	wordSet := make(map[string]bool, len(words))
	for _, word := range words {
		wordSet[word] = true
	}
	score := 0
	for _, token := range tokens {
		part := 0
		switch {
		case lowerName == token:
			part = 100
		case strings.HasPrefix(lowerName, token+"_"):
			part = 80
		case strings.HasPrefix(lowerName, token):
			part = 70
		case strings.Contains(lowerName, token):
			part = 50
		case wordSet[token]:
			part = 30
		case strings.Contains(lowerDescription, token):
			part = 10
		}
		if part == 0 {
			return 0, false
		}
		score += part
	}
	if queryLower != "" {
		if strings.Contains(lowerName, queryLower) {
			score += 40
		} else if strings.Contains(lowerDescription, queryLower) {
			score += 15
		}
	}
	return score, true
}
