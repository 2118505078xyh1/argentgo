package app

import "strings"

// CacheTier is the retention tier a prompt-cache entry is written under. The
// string values mirror the retention vocabulary used by the llm layer so a
// future caller can map a tier onto a wire hint without a lookup table.
type CacheTier string

const (
	// CacheTierNone means prompt caching is off; no marker is emitted.
	CacheTierNone CacheTier = "none"
	// CacheTierShort is the provider-default short-lived entry (5m/30m class).
	CacheTierShort CacheTier = "short"
	// CacheTierLong is the extended entry (1h/24h class).
	CacheTierLong CacheTier = "long"
)

// parseCacheTier maps a config/retention string onto a tier. It accepts both
// tier names and the TTL hints the llm layer understands ("5m"/"30m" → short,
// "1h"/"24h" → long), plus the explicit opt-outs. The boolean reports whether
// the value was recognized: an empty, unknown or non-ASCII value is not, so a
// caller can fall back to a default instead of treating it as a tier.
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

// ResolveCacheTier decides the tier a request should write under. Ported from
// omp's resolvePromptCacheTier: an explicit config tier wins; when the config is
// unset a subscription (OAuth) seat defaults to the long tier; and a long tier
// is downgraded to short when the endpoint does not support long retention.
//
// It is a pure function: no config, network or environment is consulted. The
// caller supplies isSubscription and supportsLong as already-resolved facts.
func ResolveCacheTier(configTier string, isSubscription bool, supportsLong bool) CacheTier {
	tier, explicit := parseCacheTier(configTier)
	if !explicit {
		// Subscription seats write 1h entries where the model supports them;
		// everyone else keeps the provider-default short entry.
		if isSubscription && supportsLong {
			tier = CacheTierLong
		} else {
			tier = CacheTierShort
		}
	}
	// An unsupported long request must not produce a hint the gateway cannot
	// honour: omp emits the provider's default short entry instead.
	if tier == CacheTierLong && !supportsLong {
		return CacheTierShort
	}
	return tier
}

// ObservedCacheTier infers the tier a completed response actually wrote, for
// response-side adoption. omp reads the per-TTL cache-write breakdown directly
// (usage.cache_creation.ephemeral1h/5m); argent-go only exposes aggregate
// CachedTokens/CacheWriteTokens, so the tier comes from the TTL hint the
// request carried, gated on there having been cache activity at all.
//
// A response with no cache reads and no cache writes observed nothing and
// returns CacheTierNone; so does an unrecognized or opt-out hint.
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
