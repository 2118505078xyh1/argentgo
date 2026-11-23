package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// ---- web_fetch ----

const maxFetchBytes = 200 * 1024

var (
	reBreakTags = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|header|footer|pre|table|ul|ol)\b[^>]*>`)
	reTags      = regexp.MustCompile(`(?s)<[^>]*>`)
)

type webFetchTool struct{}

func (webFetchTool) Name() string { return "web_fetch" }
func (webFetchTool) Description() string {
	return "Fetch an http(s) URL and return it as plain text. Local and private addresses are refused unless allowLocal is set."
}
func (webFetchTool) Mutating() bool { return false }
func (webFetchTool) Schema() string {
	return `{"type":"object","properties":{"url":{"type":"string","description":"http or https URL"},"allowLocal":{"type":"boolean","description":"allow loopback and private addresses"}},"required":["url"]}`
}

func (webFetchTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var args struct {
		URL        string `json:"url"`
		AllowLocal bool   `json:"allowLocal"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("web_fetch: bad arguments: %w", err)
	}
	target := strings.TrimSpace(args.URL)
	if target == "" {
		return "", fmt.Errorf("web_fetch: url must not be empty")
	}
	// 网络出口走统一安全链：策略判定 → 审批 → 审计（#5）。
	// allowLocal 只放开字面私网地址，不能覆盖显式 deny 规则或云元数据。
	if err := security.CheckNetworkEgress(ctx, target, http.MethodGet, args.AllowLocal); err != nil {
		return "", err
	}
	parsed, err := checkFetchURL(ctx, target, args.AllowLocal)
	if err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: clampTimeout("fetch", 0, maxTimeout(ctx)),
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if err := security.CheckNetworkEgress(ctx, req.URL.String(), req.Method, args.AllowLocal); err != nil {
				return err
			}
			_, redirectErr := checkFetchURL(ctx, req.URL.String(), args.AllowLocal)
			return redirectErr
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	req.Header.Set("User-Agent", "argentgo/web_fetch")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	defer resp.Body.Close()

	// 迁移自 killi 决策 12/35：在读取响应体之前先看 Content-Type。
	// 明确的二进制类型（图片/音视频/压缩包等）完全不下载，避免为
	// 大体积的非文本内容浪费带宽与内存；缺失或模糊的类型仍按原路径处理。
	if kind := resp.Header.Get("Content-Type"); fetchRejectsContentType(kind) {
		return "", fmt.Errorf("web_fetch: %s returned binary content (%s); not downloading", parsed.Redacted(), kind)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("web_fetch: %s returned %s", parsed.Redacted(), resp.Status)
	}
	truncated := len(body) > maxFetchBytes
	if truncated {
		body = truncateFetchBody(body, maxFetchBytes)
	}
	text := string(body)
	if looksHTML(resp.Header.Get("Content-Type"), text) {
		// 结构化提取：保留链接/标题/列表/表格，剔除噪声标签（纯 stdlib）。
		text = htmlToMarkdown(text)
	}
	text = strings.TrimSpace(text)
	if truncated {
		text += "\n... [truncated at 200 KB] ..."
	}
	if text == "" {
		return "(empty response)", nil
	}
	return clip(ctx, text), nil
}

// fetchRejectsContentType 判断 Content-Type 是否明确为二进制、不值得下载为文本。
// 只拒绝无歧义的类型；缺失、畸形或模糊类型一律放行，交由现有的文本转换处理，
// 避免误伤把文本资源错误标注为 application/octet-stream 的服务端。
func fetchRejectsContentType(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	switch {
	case strings.HasPrefix(mediaType, "image/"),
		strings.HasPrefix(mediaType, "audio/"),
		strings.HasPrefix(mediaType, "video/"),
		strings.HasPrefix(mediaType, "font/"):
		return true
	}
	switch mediaType {
	case "application/octet-stream", "application/zip", "application/pdf",
		"application/gzip", "application/x-tar", "application/x-7z-compressed",
		"application/x-rar-compressed", "application/wasm":
		return true
	}
	return false
}

// checkFetchURL 校验 scheme，并且除非设置了 allowLocal，否则拒绝
// 解析到回环、私有、链路本地或未指定地址主机。
func checkFetchURL(ctx context.Context, raw string, allowLocal bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: bad url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("web_fetch: unsupported scheme %q (only http and https)", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("web_fetch: url has no host")
	}
	if allowLocal {
		return parsed, nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: cannot resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return nil, fmt.Errorf("web_fetch: refusing private or local address %q (set allowLocal to override)", host)
		}
	}
	return parsed, nil
}

func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

// truncateFetchBody 把 body 切到至多 limit 字节，且不在末尾留下半个
// 多字节 UTF-8 rune。简单的 body[:limit] 可能把一个 CJK
// 字符或 emoji 切成两半；clip() 的 rune 安全头尾窗口无法修复
// 在源头就已损坏的 rune，于是坏字节会到达
// 模型。这里复用 trimPartialRune，使上限在恰好落在 rune 边界时
// 字节精确，否则丢弃整个 rune。
func truncateFetchBody(body []byte, limit int) []byte {
	if len(body) <= limit {
		return body
	}
	return body[:trimPartialRune(string(body), limit)]
}

func looksHTML(contentType, body string) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(body))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}
