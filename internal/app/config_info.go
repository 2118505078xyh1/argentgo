package app

import (
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/server"
)

// configInfoProvider 把已解析的运行配置桥接为 server.ConfigInfoProvider。
// 它在构造时通过 Resolve 取得 runner 实际使用的 model/provider/workspace，
// 使原生窗口状态栏显示真实值，而不是硬编码的假模型名。Resolve 的副作用
// 仅限读取配置与构造 chatter，不产生任何网络流量。
type configInfoProvider struct {
	info server.ConfigInfo
	// 嵌入状态适配器，使同一实例同时实现 server.StatusProvider。
	// 这样 --serve/--rpc 无需额外注入点即可复用 Resolve 的结果作为
	// /v1/status 与 RPC status 的数据源；cli 侧通过接口断言取用。
	statusProvider
}

// NewConfigInfoProvider 构造可注入 server.Config.ConfigInfoProvider 的适配器，
// 供 cmd/argent 在主会话接线时使用（--rpc / --serve / acp 的 server.Config）。
// stdio 提供环境变量（配置与凭据回退）；Resolve 在未指定 opts.Cwd 时取进程
// 当前工作目录作为 workspace。配置解析失败时返回零值适配器：config.info 会
// 回出空摘要，而不是让状态栏因为拿不到信息而崩溃。
func NewConfigInfoProvider(stdio cli.IO) server.ConfigInfoProvider {
	return newConfigInfoProvider(stdio, Resolve)
}

// newConfigInfoProvider 是 NewConfigInfoProvider 的可测试内核：resolve 作为
// 参数注入，使测试无需改动进程工作目录或磁盘配置即可验证字段映射与错误容忍。
func newConfigInfoProvider(stdio cli.IO, resolve func(cli.IO, cli.Options) (Resolved, error)) server.ConfigInfoProvider {
	resolved, err := resolve(stdio, cli.Options{})
	if err != nil {
		return &configInfoProvider{}
	}
	info := server.ConfigInfo{
		Model:     resolved.Model,
		Provider:  resolved.Provider,
		Workspace: resolved.Root,
	}
	return &configInfoProvider{
		info:           info,
		statusProvider: newStatusProviderFromConfig(info),
	}
}

func (p *configInfoProvider) ConfigInfo() server.ConfigInfo {
	return p.info
}
