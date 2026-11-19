package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---- web_search ----

const (
	webSearchURLEnv     = "ARGENT_WEB_SEARCH_URL"
	webSearchTimeout    = 20 * time.Second
	webSearchMaxResults = 20
	webSearchMaxSnippet = 500
)

type webSearchTool struct{}

func (webSearchTool) Name() string { return "web_search" }
func (webSearchTool) Description() string {
	return "Search the web through the optional ARGENT_WEB_SEARCH_URL endpoint; otherwise use web_fetch with a direct URL."
}
func (webSearchTool) Mutating() bool { return false }
func (webSearchTool) Schema() string {
	return `{"type":"object","properties":{"query":{"type":"string","description":"search terms"},"limit":{"type":"integer","description":"maximum number of results, capped at 20"}},"required":["query"]}`
}

type webSearchResult struct {
	Title   string
	URL     string
	Snippet string
}

func (webSearchTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("web_search: bad arguments: %w", err)
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "", fmt.Errorf("web_search: query must not be empty")
	}
	endpoint := strings.TrimSpace(os.Getenv(webSearchURLEnv))
	if endpoint == "" {
		return "", fmt.Errorf("web_search: %s is not configured; use web_fetch with a direct URL instead", webSearchURLEnv)
	}

	limit := args.Limit
	if limit <= 0 || limit > webSearchMaxResults {
		limit = webSearchMaxResults
	}
	payload, err := json.Marshal(map[string]any{"query": query, "limit": limit})
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: webSearchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("web_search: endpoint returned %s", resp.Status)
	}
	if len(body) > maxFetchBytes {
		body = body[:maxFetchBytes]
	}

	results, err := parseWebSearchResults(body)
	if err != nil {
		return "", err
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return clip(ctx, renderWebSearchResults(query, results)), nil
}

// parseWebSearchResults deliberately accepts several common result envelopes
// and alternative field names so a self-hosted endpoint needs no adapter.
func parseWebSearchResults(body []byte) ([]webSearchResult, error) {
	var envelope struct {
		Results json.RawMessage `json:"results"`
		Items   json.RawMessage `json:"items"`
		Data    struct {
			Results json.RawMessage `json:"results"`
			Items   json.RawMessage `json:"items"`
		} `json:"data"`
	}
	raw := body
	if err := json.Unmarshal(body, &envelope); err == nil {
		for _, candidate := range []json.RawMessage{
			envelope.Results, envelope.Items, envelope.Data.Results, envelope.Data.Items,
		} {
			if len(candidate) > 0 && string(candidate) != "null" {
				raw = candidate
				break
			}
		}
	}

	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		var one map[string]any
		if json.Unmarshal(raw, &one) == nil {
			entries = []map[string]any{one}
		} else {
			return nil, fmt.Errorf("web_search: unrecognized response shape: %w", err)
		}
	}

	results := make([]webSearchResult, 0, len(entries))
	for _, entry := range entries {
		result := webSearchResult{
			Title:   firstJSONString(entry, "title", "name"),
			URL:     firstJSONString(entry, "url", "link", "href"),
			Snippet: firstJSONString(entry, "snippet", "description", "content", "text"),
		}
		if result.Title == "" && result.URL == "" && result.Snippet == "" {
			continue
		}
		if len(result.Snippet) > webSearchMaxSnippet {
			result.Snippet = result.Snippet[:trimPartialRune(result.Snippet, webSearchMaxSnippet)]
		}
		results = append(results, result)
	}
	return results, nil
}

func firstJSONString(entry map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := entry[key].(string); ok {
			if text := strings.TrimSpace(value); text != "" {
				return text
			}
		}
	}
	return ""
}

func renderWebSearchResults(query string, results []webSearchResult) string {
	lines := []string{fmt.Sprintf("web search results for %q:", query)}
	if len(results) == 0 {
		return lines[0] + "\n(no results)"
	}
	for i, result := range results {
		line := fmt.Sprintf("%d. %s", i+1, result.Title)
		if result.URL != "" {
			line += " — " + result.URL
		}
		lines = append(lines, line)
		if result.Snippet != "" {
			lines = append(lines, "   "+result.Snippet)
		}
	}
	return strings.Join(lines, "\n")
}
