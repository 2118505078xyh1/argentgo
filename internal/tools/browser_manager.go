package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/browser"
)

// browserSessionManager 是 BrowserManager 的进程内实现：按 root 维护一个 browser 实例，
// 使 open → act → screenshot 跨工具调用共享同一 Chrome 会话。
type browserSessionManager struct {
	mu       sync.Mutex
	env      func(string) string
	sessions map[string]*browser.Browser
}

var _ BrowserManager = (*browserSessionManager)(nil)

// NewBrowserManager 构造浏览器会话管理器。env 用于读取连接配置。
func NewBrowserManager(env func(string) string) BrowserManager {
	return &browserSessionManager{env: env, sessions: make(map[string]*browser.Browser)}
}

func (m *browserSessionManager) Get(root string) (*browser.Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.sessions[root]; b != nil {
		return b, nil
	}
	connect := ""
	if m.env != nil {
		connect = strings.TrimSpace(m.env("ARGENT_BROWSER_CDP"))
	}
	b, err := browser.Open(context.Background(), browser.OpenOpts{Connect: connect})
	if err != nil {
		return nil, fmt.Errorf("browser: open: %w", err)
	}
	m.sessions[root] = b
	return b, nil
}
