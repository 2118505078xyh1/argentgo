package app

import "strings"

// CacheTier 是提示缓存条目写入时所处的保留层级。其字符串值对应 llm 层使用的保留词汇表，
// 使未来的调用方无需查找表就能把层级映射到线上提示。
type CacheTier string

const (
	// CacheTierNone 表示提示缓存关闭；不会发出任何标记。
	CacheTierNone CacheTier = "none"
	// CacheTierShort 是提供方默认的短生命周期条目（5m/30m 级别）。
	CacheTierShort CacheTier = "short"
	// CacheTierLong 是长效条目（1h/24h 级别）。
	CacheTierLong CacheTier = "long"
)

// parseCacheTier 把配置/保留字符串映射为层级。它既接受层级名，也接受 llm 层理解的 TTL 提示
// （"5m"/"30m" → short，"1h"/"24h" → long），以及显式的退出值。
// 布尔值报告该值是否被识别：空值、未知值或非 ASCII 值都不会被识别，
// 因此调用方可以回退到默认值，而不是把它当成一个层级。
func parseCacheTier(value string) (CacheTier, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "off", "false":
		return CacheTierNone, true
	case "long", "1h", "24h":
		return CacheTierLong, true
	case "short", "5m", "30m":
		return CacheTierShort, true
	default:
		return CacheTierNone, false
	}
}

// ResolveCacheTier 决定一个请求应写入的层级。移植自 omp 的 resolvePromptCacheTier：
// 显式的配置层级优先；配置未设置时，订阅（OAuth）席位默认为长效层级；
// 当端点不支持长期保留时，长效层级会被降级为短层级。
//
// 它是纯函数：不查询配置、网络或环境。调用方以已解析事实的形式提供 isSubscription 和 supportsLong。
func ResolveCacheTier(configTier string, isSubscription bool, supportsLong bool) CacheTier {
	tier, explicit := parseCacheTier(configTier)
	if !explicit {
		// 订阅席位在模型支持时写入 1 小时条目；其他情况保留提供方默认的短条目。
		if isSubscription && supportsLong {
			tier = CacheTierLong
		} else {
			tier = CacheTierShort
		}
	}
	// 不受支持的长效请求不得产生网关无法遵守的提示：omp 改为发出提供方默认的短条目。
	if tier == CacheTierLong && !supportsLong {
		return CacheTierShort
	}
	return tier
}

// ObservedCacheTier 推断一个已完成的响应实际写入的层级，供响应侧采用。
// omp 直接读取按 TTL 划分的缓存写入明细（usage.cache_creation.ephemeral1h/5m）；
// argent-go 只暴露聚合的 CachedTokens/CacheWriteTokens，因此层级来自请求携带的 TTL 提示，
// 并以确实发生过缓存活动为前提。
//
// 没有缓存读取也没有缓存写入的响应没有观察到任何活动，返回 CacheTierNone；
// 无法识别或退出提示的响应同样如此。
func ObservedCacheTier(usageCachedTokens, usageCacheWrite int, ttlHint string) CacheTier {
	tier, ok := parseCacheTier(ttlHint)
	if !ok || tier == CacheTierNone {
		return CacheTierNone
	}
	if usageCachedTokens <= 0 && usageCacheWrite <= 0 {
		return CacheTierNone
	}
	return tier
}
