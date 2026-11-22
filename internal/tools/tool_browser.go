package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/2118505078xyh1/argentgo/internal/browser"
)

// browserToolName / fastFetchToolName 供注册与测试共用。
const (
	browserToolName   = "browser"
	fastFetchToolName = "web_fastfetch"
)

// BrowserManager 管理跨工具调用存活的 browser 会话（按 root 隔离）。
// 由 app 注入；nil 时工具报告不可用（与 DAP 同语义）。
type BrowserManager interface {
	Get(root string) (*browser.Browser, error)
}

// browserTool 是面向 agent 的浏览器工具：open 建立会话并返回 @N refs 快照，
// act 对 @N ref 执行点击/输入/求值/截图，close 关闭会话。
type browserTool struct {
	mgr BrowserManager
}

var _ Tool = (*browserTool)(nil)

func (browserTool) Name() string { return browserToolName }
func (browserTool) Description() string {
	return "Drive a real headless Chrome via CDP: open a URL to get a compact snapshot with @N refs, then act on refs (click/type/eval/screenshot). For static pages prefer web_fetch; use this when JS rendering or interaction is needed."
}
func (browserTool) Mutating() bool { return true }
func (browserTool) Schema() string {
	return `{"type":"object","properties":{` +
		`"action":{"type":"string","enum":["open","snapshot","click","type","eval","screenshot","close"],"description":"browser operation"},` +
		`"url":{"type":"string","description":"URL for open"},` +
		`"ref":{"type":"string","description":"@N ref from the last snapshot (click/type)"},` +
		`"text":{"type":"string","description":"text for type, or JS expression for eval"},` +
		`"level":{"type":"string","enum":["skeleton","content","full"],"description":"extraction level (default content)"},` +
		`"full":{"type":"boolean","description":"full-page screenshot (default false)"}` +
		`},"required":["action"]}`
}

type browserArgs struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Text   string `json:"text"`
	Level  string `json:"level"`
	Full   bool   `json:"full"`
}

func (t *browserTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	if t.mgr == nil {
		return "browser integration is not available", nil
	}
	var a browserArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("browser: bad arguments: %w", err)
	}
	b, err := t.mgr.Get(root)
	if err != nil {
		return "", err
	}
	switch a.Action {
	case "open":
		if a.URL == "" {
			return "", fmt.Errorf("browser: url is required for open")
		}
		status, title, err := b.Navigate(ctx, a.URL)
		if err != nil {
			return "", err
		}
		snap, err := b.Extract(ctx, browser.ExtractLevel(a.Level))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("[%d] %s — %s\n%s", status, title, snap.URL, snap.Text), nil

	case "snapshot":
		snap, err := b.Extract(ctx, browser.ExtractLevel(a.Level))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s — %s\n%s", snap.Title, snap.URL, snap.Text), nil

	case "click":
		if a.Ref == "" {
			return "", fmt.Errorf("browser: ref is required for click")
		}
		if err := b.Click(ctx, a.Ref); err != nil {
			return "", err
		}
		return fmt.Sprintf("clicked %s", a.Ref), nil

	case "type":
		if a.Ref == "" {
			return "", fmt.Errorf("browser: ref is required for type")
		}
		if err := b.Type(ctx, a.Ref, a.Text); err != nil {
			return "", err
		}
		return fmt.Sprintf("typed into %s", a.Ref), nil

	case "eval":
		if a.Text == "" {
			return "", fmt.Errorf("browser: text (JS expression) is required for eval")
		}
		return b.Eval(ctx, a.Text)

	case "screenshot":
		png, err := b.Screenshot(ctx, a.Full)
		if err != nil {
			return "", err
		}
		// 用标记包裹 base64，便于上层识别并落盘；不嵌入正文以免撑爆上下文。
		return fmt.Sprintf("[screenshot png base64, %d bytes]\n%s", len(png), png), nil

	case "close":
		return "browser session closed by app manager", nil
	}
	return "", fmt.Errorf("browser: unknown action %q", a.Action)
}

// fastFetchTool 是匿名快速抓取（最快层：纯 HTTP，无 JS 渲染、无浏览器进程）。
type fastFetchTool struct{}

var _ Tool = (*fastFetchTool)(nil)

func (fastFetchTool) Name() string { return fastFetchToolName }
func (fastFetchTool) Description() string {
	return "Fetch a URL over plain HTTP and return readable text (no JS rendering, no browser process). Fastest layer for static pages; use browser if content needs JavaScript or interaction."
}
func (fastFetchTool) Mutating() bool { return false }
func (fastFetchTool) Schema() string {
	return `{"type":"object","properties":{"url":{"type":"string","description":"http(s) URL"}},"required":["url"]}`
}
func (fastFetchTool) Run(ctx context.Context, _ string, raw json.RawMessage) (string, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("web_fastfetch: bad arguments: %w", err)
	}
	if a.URL == "" {
		return "", fmt.Errorf("web_fastfetch: url is required")
	}
	res, err := browser.FastFetch(ctx, a.URL, browser.FastFetchOpts{})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[%d %s] %s (%d bytes)\n%s", res.Status, res.ContentType, res.URL, res.Bytes, res.Text), nil
}

// RegisterBrowser 把浏览器工具显式追加到注册表末尾（幂等）。
// 与 RegisterDebug 一致：默认不被 New/NewWithLimit 调用，golden 不变。
func RegisterBrowser(r *Registry) {
	if r == nil {
		return
	}
	for _, tool := range []Tool{&browserTool{}, fastFetchTool{}} {
		if _, exists := r.tools[tool.Name()]; exists {
			continue
		}
		r.tools[tool.Name()] = tool
		r.order = append(r.order, tool.Name())
	}
}

// SetBrowser 安装浏览器会话管理器并注册工具（幂等）。
func (r *Registry) SetBrowser(mgr BrowserManager) {
	if r == nil {
		return
	}
	RegisterBrowser(r)
	if existing, ok := r.tools[browserToolName].(*browserTool); ok {
		existing.mgr = mgr
	}
	r.wrapSchemas()
}
