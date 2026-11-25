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

	"example.com/argentgo/internal/security"
)

// ---- web_search ----

const (
	webSearchURLEnv      = "ARGENT_WEB_SEARCH_URL"
	webSearchProviderEnv = "ARGENT_WEB_SEARCH_PROVIDER"
	webSearchMaxResults  = 20
	webSearchMaxSnippet  = 500
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

	// 解析预设：显式 provider 优先，其次按已配置的 key 自动推断。
	// 没有任何信号时返回 false，下面的请求形态与接入前逐字节一致。
	preset, hasPreset := PresetForEnv(os.Getenv)

	// 用户显式设置的端点始终优先；只有未设置时才回落预设 URL。
	endpoint := strings.TrimSpace(os.Getenv(webSearchURLEnv))
	if endpoint == "" && hasPreset {
		endpoint = preset.URL
	}
	if endpoint == "" {
		return "", fmt.Errorf("web_search: %s is not configured; use web_fetch with a direct URL instead", webSearchURLEnv)
	}
	// 搜索端点也是一次出网：同样走网络策略 → 审批 → 审计（#5）。
	// 端点来自操作员配置（环境变量或预设），本地/私网端点是合法用法，
	// 因此允许字面私网地址；显式 deny 与云元数据仍然拒绝。
	if err := security.CheckNetworkEgress(ctx, endpoint, http.MethodPost, true); err != nil {
		return "", err
	}

	limit := args.Limit
	if limit <= 0 || limit > webSearchMaxResults {
		limit = webSearchMaxResults
	}

	// 只有需要 key 的预设才读取 key，且仅在确认预设后加入请求体/请求头。
	// 默认路径的字段集合与 json.Marshal 的键排序保持不变。
	var apiKey string
	if hasPreset && preset.HeaderValueEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(preset.HeaderValueEnv))
	}
	fields := map[string]any{"query": query, "limit": limit}
	if hasPreset && preset.Name == "tavily" && apiKey != "" {
		fields["api_key"] = apiKey
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("web_search: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if hasPreset && preset.HeaderName != "" && apiKey != "" {
		req.Header.Set(preset.HeaderName, apiKey)
	}
	client := &http.Client{Timeout: clampTimeout("web_search", 0, maxTimeout(ctx))}
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
		// 简单的 body[:maxFetchBytes] 可能把多字节 UTF-8 rune 切成两半，
		// 把无效字节交给 parseWebSearchResults（其 json.Unmarshal
		// 随后拒绝尾部碎片），或把一个替换 rune 泄漏
		// 到片段中。web_fetch 使用同样的 rune 安全上限；保持两者
		// 同步，使 200 KB 边界永不落在 CJK/emoji 内部。
		body = truncateFetchBody(body, maxFetchBytes)
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

// parseWebSearchResults 有意接受数种常见结果封装
// 和替代字段名，使自托管端点无需适配器。
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
