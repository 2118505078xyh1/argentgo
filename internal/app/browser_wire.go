package app

import (
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件把 browser / web_fastfetch 工具接到 app 装配面。与 DAP 一致：工具显式
// 注册（默认 New/NewWithLimit 不含它们，golden 不变），默认开启只是公布给模型；
// 真正的 Chrome 进程只在 browser.open 首次调用时由惰性管理器派生。
// ARGENT_BROWSER=0 是紧急逃生阀。

func browserToolsEnabled(env func(string) string) bool {
	return env == nil || trimEnv(env("ARGENT_BROWSER")) != "0"
}

func wireBrowser(stdio cli.IO, set *toolset) {
	if !browserToolsEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	manager := tools.NewBrowserManager(stdio.Env)
	for _, reg := range set.base.regs {
		if reg == nil {
			continue
		}
		reg.SetBrowser(manager)
	}
}
