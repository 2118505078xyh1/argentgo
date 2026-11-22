package app

import (
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件把 worktree 三件套接到 app 装配面。与 ask_user 不同，worktree 工具
// 不依赖交互终端（它们只是调用 git 子进程），因此在所有运行模式都可用；
// 但遵循同样的显式注册范式：不进默认注册表，默认 Specs()/Names() 逐字节不变。
//
// 说明：CLI 的 `--worktree` 启动 flag 仍未迁移（它涉及把会话 cwd 切进 worktree，
// 是独立的架构决策）；这里的工具让模型可以在当前会话里主动创建/列出/移除
// 隔离工作区，并用带绝对路径的文件工具在其中工作。

// worktreeToolsEnabled 判定是否注册 worktree 工具。默认开启：创建隔离工作区
// 对模型是纯增益；ARGENT_WORKTREE_TOOLS=0 时紧急关闭，保持工具列表不变。
func worktreeToolsEnabled(env func(string) string) bool {
	return env == nil || trimEnv(env("ARGENT_WORKTREE_TOOLS")) != "0"
}

func trimEnv(s string) string {
	// 内联 trim，避免为一次开关读取引入 strings 依赖（本文件保持极简）。
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\r' || s[j-1] == '\n') {
		j--
	}
	return s[i:j]
}

// wireWorktrees 为本次运行的全部工具注册表安装 worktree 三件套。
// env 关闭或工具集为空时零副作用。
func wireWorktrees(stdio cli.IO, set *toolset) {
	if !worktreeToolsEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	for _, reg := range set.base.regs {
		tools.RegisterWorktrees(reg)
	}
}
