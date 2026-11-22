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

// toolSearchTool 读取注册表自身的 spec，因此不需要依赖
// 消费这些 spec 的 agent 包。
type toolSearchTool struct {
	registry *Registry
}

func (toolSearchTool) Name() string { return "tool_search" }
func (toolSearchTool) Description() string {
	return "Discover other available tools by name or description. Use this whenever a task needs a capability not in your current tool list (e.g. git, web, LSP, memory, time, repo map). Returns up to 20 matches; call the discovered tool by its exact name afterwards."
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

// searchTokens 把查询拆为小写词。保留下划线，使
// 像 web_fetch 这样的工具名保持一个 token；忽略标点。
func searchTokens(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// toolMatchScore 给一个 spec 打分。每个 token 都必须在某处匹配：
// 含多个词的查询不应返回只匹配其中一个的工具。
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
