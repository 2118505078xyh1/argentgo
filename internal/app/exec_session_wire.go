package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件承载 TODO #120 的 app 层 opt-in 接线：仅当 ARGENT_EXEC_SESSION=1 时
// 把 write_stdin 工具追加到每个写入根注册表。默认不注册，Specs()/Names() 不变。

const execSessionEnv = "ARGENT_EXEC_SESSION"

func execSessionEnabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	return strings.TrimSpace(env(execSessionEnv)) == "1"
}

func wireExecSession(stdio cli.IO, set *toolset) {
	if !execSessionEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	for _, reg := range set.base.regs {
		tools.RegisterExecSession(reg)
	}
}
