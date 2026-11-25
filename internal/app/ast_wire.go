package app

import (
	"strings"

	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/tools"
)

// ast_wire.go 把 Go 结构编辑工具接到 app 装配面。工具本身只做预览（diff），
// 真正的写盘交给模型的 apply_patch/edit 工具，因此默认注册不引入写副作用。
// ARGENT_AST_EDIT=0 时紧急关闭（仅影响工具列表，默认工具清单 golden 不变）。

func astEditEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env("ARGENT_AST_EDIT")) != "0"
}

func wireAstEdit(stdio cli.IO, set *toolset) {
	if !astEditEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	for _, reg := range set.base.regs {
		tools.RegisterAstEdit(reg)
	}
}
