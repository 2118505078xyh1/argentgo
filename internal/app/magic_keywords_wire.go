package app

import (
	"strings"

	"example.com/argentgo/internal/agent"
)

// magicKeywordsEnv 是 magic keywords 的独立开关：只有精确等于 "1" 才启用。
// 默认关闭时 applyMagicKeywords 原样返回 system，逐字节不变。
const magicKeywordsEnv = "ARGENT_MAGIC_KEYWORDS"

// magicKeywordsEnabled 解释 ARGENT_MAGIC_KEYWORDS。env 为 nil 或未设置时关闭。
func magicKeywordsEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(magicKeywordsEnv)) == "1"
}

// applyMagicKeywords 在开启时扫描用户原文里的 magic keyword（仅 prose 生效），
// 把检测到的隐藏指令追加到系统提示。关闭或没有命中时 system 逐字节不变。
//
// 它刻意使用 rawPrompt（用户原话）而非注入了 review/context 前缀的 prompt：
// magic keyword 是用户主动表达的意图，不应被注入的模板文本触发。
func applyMagicKeywords(system, rawPrompt string, env func(string) string) string {
	if !magicKeywordsEnabled(env) {
		return system
	}
	directive := agent.MagicKeywordDirective(rawPrompt)
	if directive == "" {
		return system
	}
	return system + "\n" + directive
}
