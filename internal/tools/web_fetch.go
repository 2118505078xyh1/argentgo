package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ---- web_fetch ----

const (
	fetchTimeout  = 20 * time.Second
	maxFetchBytes = 200 * 1024
)

var (
	reDropBlocks = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)>`)
	reBreakTags  = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|header|footer|pre|table|ul|ol)\b[^>]*>`)
	reTags       = regexp.MustCompile(`(?s)<[^>]*>`)
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
	parsed, err := checkFetchURL(ctx, target, args.AllowLocal)
	if err != nil {
		return "", err
	}

	client := &http.Client{
		Timeout: fetchTimeout,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return "", fmt.Errorf("web_fetch: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("web_fetch: %s returned %s", parsed.Redacted(), resp.Status)
	}
	truncated := len(body) > maxFetchBytes
	if truncated {
		body = body[:maxFetchBytes]
	}
	text := string(body)
	if looksHTML(resp.Header.Get("Content-Type"), text) {
		text = htmlToText(text)
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

// checkFetchURL validates the scheme and, unless allowLocal is set, refuses
// hosts that resolve to loopback, private, link-local or unspecified addresses.
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

func looksHTML(contentType, body string) bool {
	if strings.Contains(strings.ToLower(contentType), "html") {
		return true
	}
	head := strings.ToLower(strings.TrimSpace(body))
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html")
}

// htmlToText drops script/style content, turns block tags into line breaks and
// removes the remaining markup.
func htmlToText(body string) string {
	body = reDropBlocks.ReplaceAllString(body, " ")
	body = reBreakTags.ReplaceAllString(body, "\n")
	body = reTags.ReplaceAllString(body, "")
	body = html.UnescapeString(body)
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
