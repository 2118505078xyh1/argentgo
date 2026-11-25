package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/jobs"
	"example.com/argentgo/internal/tools"
)

// 本文件提供后台作业注册表的最小可用接线。它把长期处于死代码状态的
// jobs.Registry 在显式 opt-in 时接到工具注册表上，使 bash 工具的
// background_after_ms 参数真正能把长命令提升为可查询的后台作业，退出时
// 再回收仍存活的进程。
//
// 默认（未 opt-in）时本文件不产生任何副作用：不创建目录、不调用 SetJobs、
// 不安装关闭钩子，jobRegistry 仍为 nil，background_after_ms 被静默忽略，
// 系统提示/事件流/会话 JSONL 逐字节不变。

// jobsOptInEnv 是启用后台作业注册表的独立环境变量。与其它一次性开关
// （ARGENT_BOOT_TOPO、ARGENT_LSP 等）一致：仅值为 "1"（允许首尾空白）时开启。
const jobsOptInEnv = "ARGENT_JOBS"

// optInJobs 是统一 opt-in 列表（ARGENT_OPT_INS）中代表后台作业注册表的键。
// 这里只在本文件声明，不改动 optin_registry.go 的集中清单，因此默认关语义
// 与集中清单完全解耦：无论从哪条激活面进入，未显式开启都为 false（fail-closed）。
const optInJobs OptInKey = "jobs"

// jobsOptInEnabled 报告本次运行是否启用后台作业注册表。
// 两条激活面取逻辑或：ARGENT_JOBS=1，或 ARGENT_OPT_INS 列表中含 "jobs"。
// env 为 nil、未知值、空列表都返回 false；不读取进程环境，不修改入参。
func jobsOptInEnabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	if strings.TrimSpace(env(jobsOptInEnv)) == "1" {
		return true
	}
	for _, field := range strings.Split(env(optInEnvVar), ",") {
		if OptInKey(strings.TrimSpace(field)) == optInJobs {
			return true
		}
	}
	return false
}

// jobRegistryDir 返回某会话的作业注册表目录：<sessionDir>/jobs。
// 作业索引 jobs.json 与日志都写在这里，随会话目录一起保留或清理。
func jobRegistryDir(sessionDir string) string {
	return filepath.Join(sessionDir, "jobs")
}

// wireJobs 为本次运行建立后台作业注册表并接到每个工具注册表上。
//
// sessionDir 通常是 filepath.Join(res.Storage, "sessions", sess.ID())；
// regs 是本次运行创建的全部工具注册表（multiRootTools 中的每一个），
// 使多写入根目录下提升的后台作业都落到同一个注册表。
//
// 返回值：
//   - registry：opt-in 开启时为新建的注册表，关闭时为 nil；
//   - stop：退出时调用，对仍运行的作业执行 StopAll(ctx)；关闭时为 no-op；
//   - err：仅 opt-in 开启且创建注册表失败时非空，调用方应中止本次运行。
//
// 默认关闭时它不触碰任何工具注册表，既有行为逐字节不变。
func wireJobs(env func(string) string, sessionDir string, regs []*tools.Registry) (*jobs.Registry, func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if !jobsOptInEnabled(env) {
		return nil, noop, nil
	}
	registry, err := jobs.New(jobRegistryDir(sessionDir))
	if err != nil {
		return nil, noop, fmt.Errorf("app: 创建后台作业注册表: %w", err)
	}
	for _, r := range regs {
		if r != nil {
			r.SetJobs(registry)
		}
	}
	return registry, registry.StopAll, nil
}
