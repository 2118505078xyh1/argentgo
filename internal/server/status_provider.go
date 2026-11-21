package server

import (
	"net/http"
	"sync"
)

// StatusProvider 由消费方（server 包）定义：实现方负责生成一段已编码好的
// 状态字节。server 侧不重新编码、不追加换行，写入响应体时保持字节原样。
//
// 这是“可选接线”的原语：接口本身不注册任何路由，是否暴露 /v1/status
// 由调用方决定。默认没有提供者时，路由不存在，行为与未启用时逐字节一致。
type StatusProvider interface {
	StatusJSON() []byte
}

// StatusMux 是状态提供者的可选注册表。它不与 server.Config 或 routes 耦合，
// 仅保存一个当前提供者。内部用 RWMutex 保护，注册与读取可安全并发进行。
type StatusMux struct {
	mu       sync.RWMutex
	provider StatusProvider
}

// RegisterStatus 注册（或替换）状态提供者。传入 nil 会清空当前提供者，
// 使 StatusJSON 回到“无提供者”状态。可安全并发调用。
func (m *StatusMux) RegisterStatus(p StatusProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.provider = p
}

// StatusJSON 返回当前提供者的字节以及是否存在提供者。
// 无提供者（未注册或已被 nil 清空）时返回 (nil, false)。
//
// 这里只在锁内取出提供者指针，随后在锁外调用 StatusJSON：
// 避免在持锁期间执行提供者代码，防止实现方重入时死锁。
func (m *StatusMux) StatusJSON() ([]byte, bool) {
	m.mu.RLock()
	p := m.provider
	m.mu.RUnlock()
	if p == nil {
		return nil, false
	}
	return p.StatusJSON(), true
}

// WriteStatus 在“存在状态提供者且请求方法为 GET”时，把提供者返回的字节
// 原样写入响应，返回 true 表示请求已被处理。
//
// 无提供者、非 GET 或 mux 为 nil 时返回 false，且完全不触碰 w；
// 调用方据此走既有的 404（无提供者/未注册路由）或 405（方法不允许）分支。
// 写入时只设置 Content-Type 与 200 状态码，响应体字节不做任何变换。
func WriteStatus(w http.ResponseWriter, r *http.Request, mux *StatusMux) bool {
	if mux == nil {
		return false
	}
	if r.Method != http.MethodGet {
		return false
	}
	body, ok := mux.StatusJSON()
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}
