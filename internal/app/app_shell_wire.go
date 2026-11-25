package app

import (
	"context"
	"strings"

	"example.com/argentgo/internal/bash"
)

// 本文件提供持久化 shell 会话的最小可用接线。它把 internal/pshell 的管理器
// 在显式 opt-in 时接到每个工具注册表上，使 bash 工具能复用长驻 shell 的
// cd / export / venv 等状态，退出时再关闭仍存活的会话。
//
// 默认（未 opt-in）时本文件不产生任何副作用：不创建 Manager、不调用
// SetShellSessions、不安装关闭钩子，工具注册表、系统提示、事件流与会话
// JSONL 逐字节不变。

// shellSessionsOptInEnv 是启用持久化 shell 会话的独立环境变量。
// 与 ARGENT_JOBS 等一次性开关一致：仅值为 "1"（允许首尾空白）时开启。
const shellSessionsOptInEnv = "ARGENT_PERSISTENT_SHELL"

// shellSessionsOptInEnabled 报告本次运行是否启用持久化 shell 会话。
// env 为 nil、未知值、空值都返回 false；不读取进程环境，不修改入参。
func shellSessionsOptInEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(shellSessionsOptInEnv)) == "1"
}

// shellManager 是 app 对持久化会话管理器的最小契约。它是 bash.Manager 的
// 子集，额外暴露 close 供退出钩子回收；测试用伪实现替换构造子即可覆盖接线。
type shellManager interface {
	Exec(ctx context.Context, workspace, shellKind, command string) (stdout string, code int, err error)
	Close() error
}

// newShellManager 是可替换的构造子；生产路径始终返回真实的 bash.Manager。
// 它是 app 中唯一 import pshell 的点，其余包保持零依赖。
var newShellManager = func() shellManager { return bash.NewManager(nil) }

// SetShellSessions 把持久化会话管理器安装到该工具集的全部注册表上。
// 参数是 any，使 tools / shell 不依赖具体会话实现包。
func (t *toolset) SetShellSessions(sessions any) {
	if t == nil || t.base == nil {
		return
	}
	for _, r := range t.base.regs {
		if r != nil {
			r.SetShellSessions(sessions)
		}
	}
}

// wireShellSessions 为本次运行建立持久化 shell 会话管理器并接到每个工具注册表。
//
// 返回值 close 在退出时调用 mgr.Close()（幂等）；默认关闭时为 no-op。
// 默认关闭时它不触碰任何工具注册表，既有行为逐字节不变。
func wireShellSessions(env func(string) string, set *toolset) func() {
	if !shellSessionsOptInEnabled(env) {
		return func() {}
	}
	mgr := newShellManager()
	set.SetShellSessions(mgr)
	return func() { _ = mgr.Close() }
}
