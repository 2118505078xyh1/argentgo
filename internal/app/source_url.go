package app

// 来源 URL 标注。aider 审计指出：web_fetch 返回的正文不带来源 URL，模型因此
// 无法在答案里明确引用抓取自哪个页面。这里提供一个纯函数，在 web_fetch /
// web_search 的工具结果前加一行已脱敏的来源标注；默认不接线，工具输出逐字节不变。

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	// sourceAnnotationPrefix / sourceAnnotationSuffix 组成单行标注模板，
	// 与每项来源用 ", " 连接，保证一次调用只新增一行。
	sourceAnnotationPrefix = "[source: "
	sourceAnnotationSuffix = "]"
	// sourceURLMaxRunes 是单个来源 URL 保留的最大符文数。超限时在符文边界
	// 截断并追加标记，CJK 与 emoji 不会被切成半个 UTF-8 字符。
	sourceURLMaxRunes      = 256
	sourceURLTruncatedMark = "…"
)

// annotateSourceURL 在 web_fetch / web_search 的成功结果前加一行来源标注。
// 来源只从工具参数（url / urls 字段）提取，不读取输出正文，因此不会把页面里
// 的任意链接误当成来源。其它工具、无 URL 的参数或无法解析的 URL 一律原样返回。
func annotateSourceURL(toolName, args, output string) string {
	if toolName != "web_fetch" && toolName != "web_search" {
		return output
	}
	urls := extractArgURLs(args)
	if len(urls) == 0 {
		return output
	}
	sanitized := make([]string, 0, len(urls))
	seen := make(map[string]bool, len(urls))
	for _, raw := range urls {
		u, ok := sanitizeSourceURL(raw)
		if !ok || seen[u] {
			continue
		}
		seen[u] = true
		sanitized = append(sanitized, u)
	}
	if len(sanitized) == 0 {
		return output
	}
	line := sourceAnnotationPrefix + strings.Join(sanitized, ", ") + sourceAnnotationSuffix
	return line + "\n" + output
}

// extractArgURLs 从 JSON 参数的 url（字符串或数组）与 urls（数组）字段按顺序
// 收集候选 URL。非 JSON 或结构不符时返回空，调用方据此原样返回输出。
func extractArgURLs(args string) []string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed[0] != '{' {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return nil
	}
	var urls []string
	for _, key := range []string{"url", "urls"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var one string
		if err := json.Unmarshal(raw, &one); err == nil {
			urls = append(urls, one)
			continue
		}
		var many []string
		if err := json.Unmarshal(raw, &many); err == nil {
			urls = append(urls, many...)
		}
	}
	return urls
}

// sanitizeSourceURL 校验候选串是 http(s) URL，去掉 userinfo，并在符文边界
// 限制长度。校验用 net/url，脱敏只在 ASCII 分隔符处切分原始串，从而保留 CJK
// 字面量不被百分号编码，也绝不会在 UTF-8 字符中间截断。
func sanitizeSourceURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if parsed.Hostname() == "" {
		return "", false
	}
	clean := stripURLUserInfo(raw)
	if utf8.RuneCountInString(clean) > sourceURLMaxRunes {
		clean = string([]rune(clean)[:sourceURLMaxRunes]) + sourceURLTruncatedMark
	}
	return clean, true
}

// stripURLUserInfo 去掉 URL authority 段中最后一个 @ 及其之前的认证信息。
// 只在 ://、@、/、?、# 这些单字节 ASCII 分隔符处切分，对多字节字符安全。
func stripURLUserInfo(raw string) string {
	sep := strings.Index(raw, "://")
	if sep < 0 {
		return raw
	}
	start := sep + 3
	end := len(raw)
	if i := strings.IndexAny(raw[start:], "/?#"); i >= 0 {
		end = start + i
	}
	authority := raw[start:end]
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return raw
	}
	return raw[:start] + authority[at+1:] + raw[end:]
}
