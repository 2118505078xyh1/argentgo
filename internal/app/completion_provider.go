package app

import (
	"example.com/argentgo/internal/predict"
	"example.com/argentgo/internal/server"
)

// completionProvider 把 predict.Engine 适配为 server.CompletionProvider：
// 仅做 Suggestion → CompletionSuggestion 的类型转换，算法与训练都在引擎内。
type completionProvider struct {
	engine *predict.Engine
}

var _ server.CompletionProvider = (*completionProvider)(nil)

// NewCompletionProvider 构造可注入 cli.App 的补全提供者。
// n 为 n-gram 阶数（上下文长度+1），默认给 4（trigram 级上下文）。
func NewCompletionProvider(n int) server.CompletionProvider {
	if n < 1 {
		n = 4
	}
	return &completionProvider{engine: predict.NewEngine(n)}
}

func (c *completionProvider) Learn(text string) { c.engine.Learn(text) }

func (c *completionProvider) Autocomplete(prefix string, limit int) []server.CompletionSuggestion {
	sugs := c.engine.Autocomplete(prefix, limit)
	out := make([]server.CompletionSuggestion, 0, len(sugs))
	for _, s := range sugs {
		out = append(out, server.CompletionSuggestion{Token: s.Token, Score: s.Score})
	}
	return out
}
