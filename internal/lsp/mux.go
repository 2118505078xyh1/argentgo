package lsp

import (
	"strings"
	"sync"
)

// muxEntry 是一条语言路由项：server 标识注册者，client 是实际处理该语言的客户端。
// 同一语言可对应多条项，按 Add 的调用顺序排列；路由时总取第一条，从而保证确定性。
type muxEntry struct {
	server string
	client *Client
}

// Mux 是按语言 id 路由语言服务器客户端的多路复用器。
// 对标 omp/oh-my-pi 的 LSP mux：多个语言服务器（如 gopls 与 rust-analyzer）
// 可在同一进程内并存，Mux 根据文件扩展名推导出的语言 id 把请求派发给负责的客户端。
// 零值可用，所有方法并发安全。
type Mux struct {
	mu          sync.RWMutex
	byLanguage  map[string][]muxEntry // 语言 id -> 按注册顺序排列的路由项
	byServer    map[string]*Client    // serverName -> client，用于关闭时按指针去重
	serverLangs map[string][]string   // serverName -> 其拥有的语言 id，用于 Remove 清理
}

// NewMux 返回一个空的多路复用器。
func NewMux() *Mux { return &Mux{} }

// Add 把 client 注册为 serverName 负责 langs 中所有语言的处理器。
// 一个 client 可负责多种语言；同一语言被多个 client 注册时，最先注册者优先。
// 以相同 serverName 重复调用会替换该 server 先前的绑定（先移除旧项再追加新项）。
// serverName 为空、client 为 nil 或 langs 全空时，Add 不产生任何绑定。
func (m *Mux) Add(serverName string, langs []string, client *Client) {
	serverName = strings.TrimSpace(serverName)
	if serverName == "" || client == nil {
		return
	}
	normalized := normalizeLanguages(langs)
	if len(normalized) == 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byLanguage == nil {
		m.byLanguage = make(map[string][]muxEntry)
	}
	if m.byServer == nil {
		m.byServer = make(map[string]*Client)
	}
	if m.serverLangs == nil {
		m.serverLangs = make(map[string][]string)
	}

	// 先清掉同一 serverName 的旧绑定，避免重复注册遗留陈旧路由项。
	m.removeLocked(serverName)

	m.byServer[serverName] = client
	m.serverLangs[serverName] = normalized
	for _, lang := range normalized {
		m.byLanguage[lang] = append(m.byLanguage[lang], muxEntry{server: serverName, client: client})
	}
}

// ClientForFile 按文件路径的扩展名推导语言 id，返回负责该语言的客户端。
func (m *Mux) ClientForFile(path string) (*Client, bool) {
	return m.ClientForLanguage(LanguageForPath(path))
}

// ClientForLanguage 返回负责 language 的客户端。
// 同一语言被多个 client 注册时按注册顺序取第一个；未注册返回 (nil, false)。
func (m *Mux) ClientForLanguage(language string) (*Client, bool) {
	lang := strings.ToLower(strings.TrimSpace(language))
	m.mu.RLock()
	defer m.mu.RUnlock()
	entries := m.byLanguage[lang]
	if len(entries) == 0 {
		return nil, false
	}
	return entries[0].client, true
}

// Remove 移除 serverName 注册的所有语言绑定，但不关闭客户端——其生命周期由调用方负责。
// 若被移除的 client 是某语言的首选，注册在它之后的其他 client 会自动成为新的首选。
func (m *Mux) Remove(serverName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(strings.TrimSpace(serverName))
}

// removeLocked 在持锁状态下清除 serverName 的所有路由项与元数据。
func (m *Mux) removeLocked(serverName string) {
	for _, lang := range m.serverLangs[serverName] {
		entries := m.byLanguage[lang]
		kept := entries[:0]
		for _, entry := range entries {
			if entry.server != serverName {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(m.byLanguage, lang)
		} else {
			m.byLanguage[lang] = kept
		}
	}
	delete(m.serverLangs, serverName)
	delete(m.byServer, serverName)
}

// Close 关闭所有已注册的客户端（按指针去重，同一客户端只关一次）并清空路由表。
// 返回关闭过程中遇到的第一个错误；多次调用安全。
func (m *Mux) Close() error {
	m.mu.Lock()
	clients := m.byServer
	m.byServer = make(map[string]*Client)
	m.byLanguage = make(map[string][]muxEntry)
	m.serverLangs = make(map[string][]string)
	m.mu.Unlock()

	unique := make(map[*Client]struct{}, len(clients))
	var first error
	for _, client := range clients {
		if _, seen := unique[client]; seen {
			continue
		}
		unique[client] = struct{}{}
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Len 返回已注册的服务器（客户端）数量。
func (m *Mux) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.byServer)
}

// normalizeLanguages 归一化语言 id：去空白、转小写、去重、忽略空值。
func normalizeLanguages(langs []string) []string {
	out := make([]string, 0, len(langs))
	seen := make(map[string]struct{}, len(langs))
	for _, lang := range langs {
		lang = strings.ToLower(strings.TrimSpace(lang))
		if lang == "" {
			continue
		}
		if _, ok := seen[lang]; ok {
			continue
		}
		seen[lang] = struct{}{}
		out = append(out, lang)
	}
	return out
}
