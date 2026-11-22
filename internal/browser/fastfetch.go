package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// FastFetchOpts 控制无头快速抓取。
type FastFetchOpts struct {
	MaxBytes  int           // 响应体上限，默认 2MB
	Timeout   time.Duration // 超时，默认 15s
	UserAgent string
}

// FastResult 是快速抓取结果。
type FastResult struct {
	URL         string
	Status      int
	ContentType string
	Text        string // 去标签后的可见文本
	Bytes       int
}

var (
	tagRE     = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg|iframe)\b[^>]*>.*?</(?:script|style|noscript|svg|iframe)>`)
	openTagRE = regexp.MustCompile(`(?s)<[^>]+>`)
	wsRE      = regexp.MustCompile(`[ \t]+`)
	nlRE      = regexp.MustCompile(`\n{3,}`)
)

// FastFetch 用纯 HTTP 抓取 URL 并抽取可见文本（无 JS 渲染、无浏览器进程）。
// 这是分层里的最快层：静态 HTML 用它即可，JS 渲染页再升级到 Open+Navigate。
func FastFetch(ctx context.Context, url string, opts FastFetchOpts) (*FastResult, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("browser: fastfetch requires http(s) URL")
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 2 << 20
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "Mozilla/5.0 (compatible; argentgo-fastfetch/1.0)"
	}
	cctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(opts.MaxBytes)))
	if err != nil {
		return nil, err
	}
	ct := resp.Header.Get("Content-Type")
	text := htmlToText(string(raw))
	return &FastResult{
		URL: url, Status: resp.StatusCode, ContentType: ct,
		Text: text, Bytes: len(raw),
	}, nil
}

// htmlToText 把 HTML 转成紧凑可见文本：去 script/style、去标签、合并空白。
func htmlToText(s string) string {
	s = tagRE.ReplaceAllString(s, " ")
	// 块级标签替换为换行，保留文本分段。
	block := regexp.MustCompile(`(?i)</?(p|div|li|h[1-6]|tr|br|section|article|header|footer|table)\b[^>]*>`)
	s = block.ReplaceAllString(s, "\n")
	s = openTagRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = wsRE.ReplaceAllString(strings.TrimSpace(l), " ")
		lines[i] = l
	}
	s = strings.Join(lines, "\n")
	return strings.TrimSpace(nlRE.ReplaceAllString(s, "\n\n"))
}
