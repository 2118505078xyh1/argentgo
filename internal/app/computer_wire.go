package app

import (
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/tools"
)

// 本文件把 computer 工具接到 app 装配面。与 DAP/browser 一致：工具显式注册
// （默认 New/NewWithLimit 不含它，golden 不变），默认开启只是公布给模型；
// 真正的桌面操作只在工具被调用时发生，且工具整体 mutating=true，走现有审批链。
// ARGENT_COMPUTER=0 是紧急逃生阀。
//
// 分层：Web → browser（文本 refs，快）；原生桌面应用 → computer（截图+坐标，通用）。

func computerToolsEnabled(env func(string) string) bool {
	return env == nil || trimEnv(env("ARGENT_COMPUTER")) != "0"
}

func wireComputer(stdio cli.IO, set *toolset) {
	if !computerToolsEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	for _, reg := range set.base.regs {
		if reg == nil {
			continue
		}
		tools.RegisterComputer(reg)
	}
}
