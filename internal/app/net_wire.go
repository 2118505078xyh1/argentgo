package app

import (
	"context"
	"strings"

	"example.com/argentgo/internal/config"
	"example.com/argentgo/internal/security"
)

// netPolicyEnv 是网络出口策略的紧急回退开关：只有显式设为 "0" 才忽略配置
// 里的 net.rules，回退到内置默认策略（私网/云元数据拒绝，其余放行）。
const netPolicyEnv = "ARGENT_NET_POLICY"

// netPolicyEnabled 报告是否采用配置中的声明式网络规则。默认开启；=0 回退。
func netPolicyEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env(netPolicyEnv)) != "0"
}

// networkGuardFrom 把配置里的 net.rules 转换成 NetworkGuard 并注入本轮 ctx，
// 覆盖 web_fetch / web_search / MCP HTTP-SSE / shell 子进程代理四条出网出口。
//
// 没有配置规则或开关关闭时仍注入一个带审批链的守卫：这样 Ask 判定能走真人
// 审批、deny/allow 能进审计（而非空链Ask被拒、deny 不记审计）。这与未注入
// 时退化为 defaultNetworkGuard（无链）的行为相比只增强不放松。
func networkGuardFrom(raw map[string]any, chain *security.Chain, env func(string) string) *security.NetworkGuard {
	rules := []security.NetRule{}
	if netPolicyEnabled(env) {
		for _, r := range config.ParseNetConfig(raw).Rules {
			decision, ok := security.ParseDecision(r.Decision)
			if !ok {
				continue
			}
			rules = append(rules, security.NetRule{
				Decision:     decision,
				HostGlob:     r.HostGlob,
				Port:         r.Port,
				Protocol:     r.Protocol,
				Method:       r.Method,
				RequireHuman: r.RequireHuman,
			})
		}
	}
	policy := security.NewNetPolicy(security.NetPolicyConfig{Rules: rules})
	return security.NewNetworkGuard(policy, chain)
}

// withNetworkGuard 把网络守卫注入 Execute 的 ctx。chain 由本轮装配，确保
// 不同会话/运行的审批链彼此隔离（与 netproxy.DefaultEnv 的按链缓存一致）。
func withNetworkGuard(ctx context.Context, raw map[string]any, chain *security.Chain, env func(string) string) context.Context {
	return security.WithNetworkGuard(ctx, networkGuardFrom(raw, chain, env))
}
