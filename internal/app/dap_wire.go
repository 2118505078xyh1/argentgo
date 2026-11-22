package app

import (
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件把 DAP 调试工具接到 app 装配面。工具是显式注册的：默认注册表
// （tools.New/NewWithLimit）不含它们，golden 逐字节不变；默认开启只是把工具
// 公布给模型，并不启动任何调试进程——真正的 adapter 只在 debug_start 被调用
// 时才由注入的会话管理器派生。ARGENT_DAP=0 是紧急逃生阀。

// dapToolsEnabled 判定是否注册调试工具。默认开启；ARGENT_DAP=0（允许首尾空白）关闭。
func dapToolsEnabled(env func(string) string) bool {
	return env == nil || trimEnv(env("ARGENT_DAP")) != "0"
}

// wireDap 为本次运行的全部工具注册表安装调试工具。管理器只构造一次并注入
// 所有注册表，使工具之间共享同一组进程内会话。env 关闭或工具集为空时零副作用。
func wireDap(stdio cli.IO, set *toolset) {
	if !dapToolsEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	manager := tools.NewDapSessionManager(stdio.Env)
	for _, reg := range set.base.regs {
		if reg == nil {
			continue
		}
		tools.RegisterDebug(reg)
		reg.SetDap(manager)
	}
}
