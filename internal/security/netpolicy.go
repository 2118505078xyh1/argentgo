package security

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// 本文件实现声明式网络出口策略（TODO #5）。规则是可序列化的纯数据，
// CheckURL 是不触碰网络、不读环境、不加锁的纯函数：它只解析 URL 字面量，
// 因此既能被工具出口在 hot path 上反复调用，也能被测试穷举。
//
// 设计要点：
//   - 复用 GlobMatch 做主机模式匹配（大小写不敏感、支持 * 与 **）；
//   - 多条规则命中时取最严格判定（deny > ask > allow），allow 永远不会
//     缓和 deny/ask；
//   - 硬编码的私网/云元数据拒绝先于用户规则运行，即使 allowLocal 也不能
//     借规则放行云元数据（allowLocal 只能把 private_address 降级，见
//     CheckNetworkEgress）。

// NetReason 是结构化的拒绝原因代码。审计与工具错误都携带它，
// 让模型与操作员能区分"策略拒绝"和"内网地址"这类不同的失败。
type NetReason string

const (
	NetReasonNone              NetReason = ""
	NetReasonMalformedURL      NetReason = "malformed_url"
	NetReasonUnsupportedScheme NetReason = "unsupported_scheme"
	NetReasonMissingHost       NetReason = "missing_host"
	NetReasonPrivateAddress    NetReason = "private_address"
	NetReasonCloudMetadata     NetReason = "cloud_metadata"
	NetReasonPolicyDeny        NetReason = "policy_deny"
)

// NetRule 是一条网络出口规则。空字段是通配符；Port 为零表示不限端口。
// Protocol 与 Method 均为小写/大写不敏感比较。
type NetRule struct {
	Decision     Decision
	HostGlob     string
	Port         int
	Protocol     string
	Method       string
	RequireHuman bool // 命中该规则时强制真人审批（#7）
}

// NetResult 携带判定、可读原因、结构化代码与命中的规则。
type NetResult struct {
	Decision Decision
	Reason   string
	Code     NetReason
	Rule     *NetRule
}

// NetPolicyConfig 是声明式规则集。零值表示只保留内置的私网拒绝。
type NetPolicyConfig struct {
	Rules []NetRule
}

// NetPolicy 是只读的网络策略。构造后不再修改，可并发调用 CheckURL。
type NetPolicy struct {
	rules []NetRule
}

// NewNetPolicy 构造策略。调用方负责复制规则切片；这里再复制一份以防误改。
func NewNetPolicy(cfg NetPolicyConfig) *NetPolicy {
	return &NetPolicy{rules: append([]NetRule(nil), cfg.Rules...)}
}

// DefaultNetPolicy 返回不带用户规则的默认策略：只拒绝字面私网地址与云
// 元数据端点，其余公网地址默认放行（与 web_fetch 的历史行为一致）。
func DefaultNetPolicy() *NetPolicy { return NewNetPolicy(NetPolicyConfig{}) }

// CheckURL 是包级便捷函数，等价于 DefaultNetPolicy().CheckURL。
func CheckURL(raw, method string) NetResult { return DefaultNetPolicy().CheckURL(raw, method) }

// CheckURL 解析 URL 并返回判定。它不解析 DNS：主机名即使解析到私网也由
// 工具层已有的解析器守卫处理（见 web_fetch.checkFetchURL）；这里只拒绝
// 字面 IP 形式的内网/云元数据地址，保证函数纯度与跨调用可复现。
func (p *NetPolicy) CheckURL(raw, method string) NetResult {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return NetResult{Deny, "malformed url", NetReasonMalformedURL, nil}
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return NetResult{Deny, "unsupported url scheme " + strconv.Quote(parsed.Scheme), NetReasonUnsupportedScheme, nil}
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return NetResult{Deny, "url has no host", NetReasonMissingHost, nil}
	}
	verb := strings.ToUpper(strings.TrimSpace(method))
	if verb == "" {
		verb = "GET"
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	if text := parsed.Port(); text != "" {
		if n, err := strconv.Atoi(text); err == nil && n > 0 && n <= 65535 {
			port = n
		} else {
			return NetResult{Deny, "invalid port in url", NetReasonMalformedURL, nil}
		}
	}

	// 内置拒绝先于用户规则：私网与云元数据永远不能被 allow 规则放开。
	if reason, code := builtinHostDeny(host); code != NetReasonNone {
		return NetResult{Deny, reason, code, nil}
	}

	rules := p.rules
	if rules == nil {
		return NetResult{Allow, "", NetReasonNone, nil}
	}
	var best Decision
	matched := false
	var reason string
	var matchedRule *NetRule
	for i := range rules {
		rule := &rules[i]
		if rule.HostGlob != "" && !GlobMatch(rule.HostGlob, host) {
			continue
		}
		if rule.Port > 0 && rule.Port != port {
			continue
		}
		if rule.Protocol != "" && !strings.EqualFold(rule.Protocol, scheme) {
			continue
		}
		if rule.Method != "" && !strings.EqualFold(rule.Method, verb) {
			continue
		}
		// deny 最严格，命中即可停止扫描：后面的 allow 无法覆盖它。
		if rule.Decision == Deny {
			return NetResult{Deny, netRuleReason(rule, host), NetReasonPolicyDeny, rule}
		}
		if !matched || rule.Decision > best {
			best = rule.Decision
			reason = netRuleReason(rule, host)
			matchedRule = rule
		}
		matched = true
	}
	if !matched {
		return NetResult{Allow, "", NetReasonNone, nil}
	}
	return NetResult{best, reason, NetReasonNone, matchedRule}
}

// netRuleReason 生成可读的命中理由；ask 规则必须带上理由供审批提示使用。
func netRuleReason(rule *NetRule, host string) string {
	if rule.Decision == Ask {
		return "network egress to " + strconv.Quote(host) + " requires approval per the network policy"
	}
	return "network egress to " + strconv.Quote(host) + " is denied by the network policy"
}

// builtinHostDeny 返回字面 IP 或已知元数据主机名的内置拒绝。
// 这里刻意不解析 DNS：纯函数语义优先，DNS 解析由调用方在需要时完成。
func builtinHostDeny(host string) (string, NetReason) {
	switch host {
	case "169.254.169.254", "metadata.google.internal", "metadata.goog", "instance-data":
		return "network egress to the cloud metadata endpoint is denied", NetReasonCloudMetadata
	}
	// IPv6 链路本地元数据地址（AWS IMDSv2）。
	if strings.EqualFold(host, "fd00:ec2::254") {
		return "network egress to the cloud metadata endpoint is denied", NetReasonCloudMetadata
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", NetReasonNone
	}
	if blockedNetIP(ip) {
		return "network egress to a private or local address is denied", NetReasonPrivateAddress
	}
	return "", NetReasonNone
}

// blockedNetIP 与 web_fetch 的 blockedIP 保持同一口径，避免两处对"内网"
// 的定义漂移。放在 security 包内供网络策略复用。
func blockedNetIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}
