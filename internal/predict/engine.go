package predict

import (
	"sync"
)

// Engine 是 Model 的线程安全封装，面向 RPC / TUI 等并发调用方：
// Learn（从历史文本学习）与 Autocomplete（按前缀补全）可并发调用，
// 内部用 RWMutex 保护——多个补全可并行，学习独占。
//
// 它与裸 Model 的分工：Model 是可独立使用的纯算法（便于表驱动测试），
// Engine 是接线层需要的并发安全门面。
type Engine struct {
	mu    sync.RWMutex
	model *Model
}

// NewEngine 构造一个 n 阶补全引擎。n 语义与 New 相同（n=上下文长度+1）。
func NewEngine(n int) *Engine {
	return &Engine{model: New(n)}
}

// Learn 从文本学习 n-gram 转移分布。空文本是空操作。
func (e *Engine) Learn(text string) {
	if text == "" {
		return
	}
	e.mu.Lock()
	e.model.Learn(text)
	e.mu.Unlock()
}

// Autocomplete 返回前缀下最可能的 limit 个后继 token。
func (e *Engine) Autocomplete(prefix string, limit int) []Suggestion {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.model.Predict(prefix, limit)
}
