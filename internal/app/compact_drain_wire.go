package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/agent"
)

// 压缩后逐条淘汰的启用开关（项目 TODO #126）。默认关闭时恢复路径逐字节不变。
const compactDrainEnableEnv = "ARGENT_COMPACT_DRAIN"

func compactDrainEnabledFromEnv(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env(compactDrainEnableEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func configureCompactDrain(env func(string) string) {
	agent.SetCompactDrain(compactDrainEnabledFromEnv(env))
}
