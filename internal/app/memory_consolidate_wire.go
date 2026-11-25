package app

import (
	"strings"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/tools"
)

// 本文件把 memory_consolidate 接到 app 装配面。默认关闭：整合会改写记忆日志，
// 只有 ARGENT_MEMORY_CONSOLIDATE=1 时才注册工具。
func memoryConsolidateEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_MEMORY_CONSOLIDATE")) == "1"
}

func wireMemoryConsolidate(stdio cli.IO, set *toolset) {
	if !memoryConsolidateEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	for _, reg := range set.base.regs {
		tools.RegisterMemoryConsolidate(reg)
	}
}
