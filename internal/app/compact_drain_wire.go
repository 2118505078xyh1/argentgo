package app

import (
	"strings"

	"example.com/argentgo/internal/agent"
)

// 压缩后逐条淘汰的启用开关（项目 TODO #126）。默认关闭时恢复路径逐字节不变。
const compactDrainEnableEnv = "ARGENT_COMPACT_DRAIN"

func compactDrainEnabledFromEnv(env func(string) string) bool {
	if env == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(env(compactDrainEnableEnv))) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

func configureCompactDrain(env func(string) string) {
	agent.SetCompactDrain(compactDrainEnabledFromEnv(env))
}
