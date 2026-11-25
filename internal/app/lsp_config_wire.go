package app

import (
	"example.com/argentgo/internal/config"
	"example.com/argentgo/internal/lsp"
)

// 本文件把 config 解析出的 lsp 块接进进程级 LSP 管理器（#43）。
// 它只做装配：七层优先级由 config.Load 完成，服务器派生仍由 lsp 客户端惰性执行。
//
// 信任门控：项目级配置可以指定任意可执行程序，因此 LSPFromProject() 为真且
// 本次运行未开启 --trust-project 时，整段项目配置被忽略（fail-closed），
// 只保留内置默认服务器。全局配置、环境变量、flag 层的 lsp 设置不受影响。
//
// 接线点：Execute 在装配 chatter/toolset 之后、首次 lsp_* 调用之前调用
// wireLSPFromConfig(res)；未调用时行为与接线前完全一致（默认注册表）。

// wireLSPFromConfig 把一次运行的配置应用到共享 LSP 注册表。
func wireLSPFromConfig(res Resolved) {
	raw := res.Cfg.Raw
	if res.Cfg.LSPFromProject() && !res.TrustProject {
		// 项目级 LSP 配置未受信：用空块重建，只保留内置默认服务器。
		raw = nil
	}
	cfg := config.ParseLSPConfig(raw)
	registry := lsp.NewRegistryFromConfig(cfg)

	mgr := getSharedManager()
	mgr.mu.Lock()
	mgr.reg = registry
	mgr.mu.Unlock()
}
