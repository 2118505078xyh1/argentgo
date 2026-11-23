package security

import (
	"context"
	"encoding/json"
	"fmt"
)

// 网络出口守卫（TODO #5 的接线层）：把 NetPolicy 的纯判定接到 Chain 的
// "决策 → 审批 → 审计" 三段。工具与 MCP 传输层只需在真正建连之前调用
// CheckNetworkEgress；无守卫注入时退化为默认策略的纯判定，仍会拒绝
// 私网/策略拒绝域名，因此默认路径不会更宽松。

const networkEgressTool = "network_egress"

// NetworkGuard 持有一张网络策略与可选的审批链。
type NetworkGuard struct {
	Policy *NetPolicy
	Chain  *Chain
	// Tool 是审计与审批提示中显示的动作名；为空时使用 network_egress。
	Tool string
}

// NewNetworkGuard 构造守卫。policy 为 nil 时使用默认策略；chain 为 nil 时
// Ask 判定会被拒绝（没有审批通道，不能静默放行）。
func NewNetworkGuard(policy *NetPolicy, chain *Chain) *NetworkGuard {
	if policy == nil {
		policy = DefaultNetPolicy()
	}
	return &NetworkGuard{Policy: policy, Chain: chain}
}

// networkGuardKey 是 NetworkGuard 的私有 context key。
type networkGuardKey struct{}

// WithNetworkGuard 把守卫注入 ctx，供下游工具的出口检查读取。
func WithNetworkGuard(ctx context.Context, guard *NetworkGuard) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, networkGuardKey{}, guard)
}

// NetworkGuardFrom 返回 ctx 中的守卫；未注入时返回 nil。
func NetworkGuardFrom(ctx context.Context) *NetworkGuard {
	if ctx == nil {
		return nil
	}
	guard, _ := ctx.Value(networkGuardKey{}).(*NetworkGuard)
	return guard
}

// defaultNetworkGuard 是未显式注入守卫时的兜底：默认策略 + 无审批链。
var defaultNetworkGuard = NewNetworkGuard(nil, nil)

// CheckNetworkEgress 在建立网络连接之前运行一次完整的出口检查。
//
// allowLocal 只能把"字面私网地址"降级为放行（保留 web_fetch 既有语义），
// 不能覆盖显式 deny 规则、云元数据或畸形 URL。
func CheckNetworkEgress(ctx context.Context, rawURL, method string, allowLocal bool) error {
	guard := NetworkGuardFrom(ctx)
	if guard == nil {
		guard = defaultNetworkGuard
	}
	policy := guard.Policy
	if policy == nil {
		policy = DefaultNetPolicy()
	}
	result := policy.CheckURL(rawURL, method)
	if allowLocal && result.Code == NetReasonPrivateAddress {
		// allowLocal 是 web_fetch 的历史逃生口：只放开私网字面地址，
		// 显式 deny、云元数据和格式错误仍然拒绝。
		result = NetResult{Decision: Allow, Code: NetReasonNone}
	}
	args, _ := json.Marshal(map[string]string{"url": rawURL, "method": method})
	name := guard.Tool
	if name == "" {
		name = networkEgressTool
	}
	switch result.Decision {
	case Deny:
		reason := result.Reason
		if reason == "" {
			reason = "network egress denied by policy"
		}
		if guard.Chain != nil {
			guard.Chain.RecordDecision(name, "deny", reason, args)
		}
		return &StopError{Tool: name, Reason: reason}
	case Ask:
		if guard.Chain == nil {
			reason := "network egress requires approval but no approval chain is configured"
			return &StopError{Tool: name, Reason: reason}
		}
		checkCtx := ctx
		if result.Rule != nil && result.Rule.RequireHuman {
			checkCtx = WithRequireHuman(checkCtx)
		}
		if err := guard.Chain.Check(checkCtx, name, args); err != nil {
			return fmt.Errorf("network egress to %q blocked: %w", rawURL, err)
		}
		return nil
	default:
		if guard.Chain != nil {
			guard.Chain.RecordDecision(name, "allow", "network egress allowed by policy", args)
		}
		return nil
	}
}
