package auth

import "time"

// ExpiringTokens 列出其存储凭据即将
// 在接下来 `within` 窗口内过期的 provider ID，包括
// 已经过期的凭据。它是只读诊断辅助：结果只包含
// provider ID，绝不包含访问或刷新秘密，因此可安全记录、显示于
// UI 或放入支持包。
//
// 凭据剩余生命周期至多为 `within` 时算作即将过期；
// `within`；因此非正 `within` 把扫描收窄到在 `now` 时
// `now` 时已过期的凭据。负的 `within` 与零相同。
// 没有截止时间的条目（API key）和没有秘密的条目被跳过，
// 因为它们永不过期。`now` 是显式的，使调用方和测试共享一个
// 时钟，分类复用 TokenStatus 使余量和溢出
// 规则在整个包中保持一致。
//
// 规则在整个包中保持一致。
// 结果遵循 store 的 provider 顺序。若 store 无法读取，ExpiringTokens 返回 nil 而非静默的空列表，使损坏的
// 凭据文件永不被误认为健康。
func ExpiringTokens(store *Store, now time.Time, within time.Duration) []string {
	tokens, err := store.List()
	if err != nil {
		return nil
	}
	margin := int64(within / time.Second)
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if token.Bearer() == "" || token.ExpiresAt.IsZero() {
			continue
		}
		switch state := TokenStatus(token.ExpiresAt.Unix(), now.Unix(), margin); state {
		case TokenExpiring, TokenExpired:
			out = append(out, token.Provider)
		}
	}
	return out
}
