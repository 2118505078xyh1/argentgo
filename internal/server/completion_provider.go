package server

// CompletionProvider 是 spelling.autocomplete RPC 所需的补全能力的最小切面。
// 由 app 层注入（内部用 internal/predict 的 Engine 实现），server 包不 import
// predict：窄接口避免分层耦合，也便于测试用 fake 替代。
type CompletionProvider interface {
	// Learn 让补全引擎从一段文本（通常是用户提示或最近编辑的文件）学习。
	Learn(text string)
	// Autocomplete 返回 prefix 下最多 limit 条补全建议（token 与分数）。
	Autocomplete(prefix string, limit int) []CompletionSuggestion
}

// CompletionSuggestion 是一条补全建议的线上投影。
type CompletionSuggestion struct {
	Token string  `json:"token"`
	Score float64 `json:"score"`
}
