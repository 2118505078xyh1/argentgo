// Package browser 提供一个纯标准库、面向 LLM agent 的 CDP 浏览器驱动。
//
// 设计参考 ghostchrome（dev-toolings/ghostchrome）：只过滤出可交互元素并分配
// @N refs，文本优先输出，避免把完整 a11y 树塞进模型上下文。底层连接用
// internal/cdp（同样是 stdlib-only 的 WebSocket + CDP 实现），不引入 rod /
// puppeteer / Node。
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"example.com/argentgo/internal/cdp"
	"example.com/argentgo/internal/proc"
)

// Browser 持有一条到 Chrome 的 CDP 连接与当前活动页面的 session id。
type Browser struct {
	ws       string                  // browser-level websocket（来自 /json/version）
	sessions map[string]*cdp.Session // target session id -> session
	active   string                  // 当前活动 target session id
	cmd      *exec.Cmd               // 自启动的 Chrome 进程（nil 表示 attach 现有）
}

// OpenOpts 描述连接或启动浏览器。
type OpenOpts struct {
	Connect  string // 现有 CDP 地址（http://host:port）；空则自动启动本地 Chrome
	Port     int    // 自启动时的调试端口，默认 9333
	Headless bool   // 自启动时是否无头，默认 true
}

// Open 连接现有 CDP 端点或启动一个新 Chrome。
func Open(ctx context.Context, opts OpenOpts) (*Browser, error) {
	base := strings.TrimRight(opts.Connect, "/")
	if base == "" {
		base = "http://127.0.0.1:9333"
	}
	if opts.Port == 0 {
		opts.Port = 9333
	}
	// 探测端点是否已有 Chrome；没有则尝试启动。
	if !endpointAlive(ctx, base) {
		if opts.Connect != "" {
			return nil, fmt.Errorf("browser: CDP endpoint %s unreachable", base)
		}
		if err := launchChrome(ctx, opts.Port, opts.Headless); err != nil {
			return nil, err
		}
	}
	version, err := getJSON(ctx, base+"/json/version")
	if err != nil {
		return nil, fmt.Errorf("browser: get version: %w", err)
	}
	ws, _ := version["webSocketDebuggerUrl"].(string)
	if ws == "" {
		return nil, errors.New("browser: no webSocketDebuggerUrl")
	}
	b := &Browser{ws: ws, sessions: make(map[string]*cdp.Session), active: ""}
	// 取一个已有页面或新建一个 about:blank。
	targets, err := getJSONArray(ctx, base+"/json/list")
	if err != nil {
		return nil, fmt.Errorf("browser: list targets: %w", err)
	}
	pageWS := ""
	for _, t := range targets {
		if kind, _ := t["type"].(string); kind == "page" {
			pageWS, _ = t["webSocketDebuggerUrl"].(string)
			break
		}
	}
	if pageWS == "" {
		nt, err := getJSON(ctx, base+"/json/new?about:blank")
		if err == nil {
			pageWS, _ = nt["webSocketDebuggerUrl"].(string)
		}
	}
	if pageWS == "" {
		return nil, errors.New("browser: no page target available")
	}
	sess, err := cdp.Dial(ctx, pageWS)
	if err != nil {
		return nil, fmt.Errorf("browser: attach page: %w", err)
	}
	b.sessions["page"] = sess
	b.active = "page"
	return b, nil
}

func (b *Browser) session() (*cdp.Session, error) {
	s := b.sessions[b.active]
	if s == nil {
		return nil, errors.New("browser: no active page")
	}
	return s, nil
}

// Navigate 导航当前页面到 url 并等待加载完成。
func (b *Browser) Navigate(ctx context.Context, url string) (status int, title string, err error) {
	s, err := b.session()
	if err != nil {
		return 0, "", err
	}
	result, err := s.Call(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return 0, "", err
	}
	var nav struct {
		ErrorText string `json:"errorText"`
	}
	_ = json.Unmarshal(result, &nav)
	if nav.ErrorText != "" {
		return 0, "", fmt.Errorf("browser: navigate: %s", nav.ErrorText)
	}
	// 等待 document.readyState 完成（轮询而非事件，简单可靠）。
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, e := s.Call(ctx, "Runtime.evaluate", map[string]any{
			"expression": "document.readyState", "returnByValue": true,
		})
		if e == nil {
			var rs struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			if json.Unmarshal(res, &rs) == nil && rs.Result.Value == "complete" {
				break
			}
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	title = pageTitle(ctx, s)
	return 200, title, nil
}

func pageTitle(ctx context.Context, s *cdp.Session) string {
	res, err := s.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": "document.title", "returnByValue": true,
	})
	if err != nil {
		return ""
	}
	var r struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if json.Unmarshal(res, &r) == nil {
		return r.Result.Value
	}
	return ""
}

// Close 关闭页面会话与自启动的 Chrome。
func (b *Browser) Close() error {
	for _, s := range b.sessions {
		_ = s.Close()
	}
	if b.cmd != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	return nil
}

func endpointAlive(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func getJSON(ctx context.Context, url string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

func getJSONArray(ctx context.Context, url string) ([]map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []map[string]any
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out, err
}

// launchChrome 在 PATH 与常见安装位置查找 Chrome/Edge 并启动无头实例。
func launchChrome(ctx context.Context, port int, headless bool) error {
	exe := findChrome()
	if exe == "" {
		return errors.New("browser: no Chrome/Chromium/Edge found; set ARGENT_BROWSER_CHROME or connect to a CDP endpoint")
	}
	profile, err := os.MkdirTemp("", "argentgo-cdp-")
	if err != nil {
		return err
	}
	args := []string{
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--no-first-run", "--no-default-browser-check", "--disable-gpu",
		"--user-data-dir=" + profile,
	}
	if headless {
		args = append(args, "--headless=new")
	}
	args = append(args, "about:blank")
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = proc.SanitizeEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("browser: launch chrome: %w", err)
	}
	// 等端口就绪。
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if endpointAlive(context.Background(), base) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("browser: chrome started but CDP endpoint not ready")
}

func findChrome() string {
	if p := strings.TrimSpace(os.Getenv("ARGENT_BROWSER_CHROME")); p != "" {
		return p
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		candidates = []string{
			filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		}
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	default:
		candidates = []string{"google-chrome", "chromium", "chromium-browser"}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// freePort 返回一个空闲端口（测试用）。
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
