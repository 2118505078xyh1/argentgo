package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// 子代理共享 system prompt 的启用开关。迁移自 killi 决策 20：
// 开启后子代理用父级 system 作为前缀，首请求复用缓存；角色约束仍在末尾。
const subagentSharedPromptEnableEnv = "ARGENT_SUBAGENT_SHARED_PROMPT"

func subagentSharedPromptEnabledFromEnv(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env(subagentSharedPromptEnableEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// configureSubagentSharedPrompt 在每次 Execute 入口按本次 env 设置开关，
// 使进程级原子状态由本次运行唯一决定，不受上次运行残留影响。
func configureSubagentSharedPrompt(env func(string) string) {
	agent.SetSubagentSharedPrompt(subagentSharedPromptEnabledFromEnv(env))
}
