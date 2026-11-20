package app

import "testing"

func TestResolveCacheTierExplicitWins(t *testing.T) {
	tests := []struct {
		name           string
		config         string
		isSubscription bool
		supportsLong   bool
		want           CacheTier
	}{
		{"explicit short", "short", true, true, CacheTierShort},
		{"short ttl hint", "5m", true, true, CacheTierShort},
		{"other short ttl hint", "30m", true, true, CacheTierShort},
		{"explicit long", "long", false, true, CacheTierLong},
		{"long ttl hint", "1h", false, true, CacheTierLong},
		{"other long ttl hint", "24h", false, true, CacheTierLong},
		{"explicit none beats subscription default", "none", true, true, CacheTierNone},
		{"off alias", "off", true, true, CacheTierNone},
		{"false alias", "false", true, true, CacheTierNone},
		{"whitespace and case tolerated", "  LONG ", true, true, CacheTierLong},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveCacheTier(tt.config, tt.isSubscription, tt.supportsLong); got != tt.want {
				t.Fatalf("ResolveCacheTier(%q, %v, %v) = %q, want %q", tt.config, tt.isSubscription, tt.supportsLong, got, tt.want)
			}
		})
	}
}

func TestResolveCacheTierSubscriptionDefaultAndDowngrade(t *testing.T) {
	// Unset config: a subscription seat defaults to long only when the endpoint
	// supports it; everyone else stays short.
	if got := ResolveCacheTier("", true, true); got != CacheTierLong {
		t.Fatalf("subscription default = %q, want long", got)
	}
	if got := ResolveCacheTier("", true, false); got != CacheTierShort {
		t.Fatalf("subscription without long support = %q, want short", got)
	}
	if got := ResolveCacheTier("", false, true); got != CacheTierShort {
		t.Fatalf("non-subscription default = %q, want short", got)
	}
	if got := ResolveCacheTier("", false, false); got != CacheTierShort {
		t.Fatalf("non-subscription without long support = %q, want short", got)
	}

	// An explicit long request is downgraded when long retention is unavailable.
	if got := ResolveCacheTier("long", true, false); got != CacheTierShort {
		t.Fatalf("explicit long without support = %q, want short downgrade", got)
	}
	if got := ResolveCacheTier("1h", false, false); got != CacheTierShort {
		t.Fatalf("explicit 1h hint without support = %q, want short downgrade", got)
	}
	// A short request is never upgraded, even for a subscription seat.
	if got := ResolveCacheTier("short", true, true); got != CacheTierShort {
		t.Fatalf("explicit short on subscription = %q, want short", got)
	}
}

func TestResolveCacheTierUnknownFallsBackToDefault(t *testing.T) {
	// An unrecognized value is treated as unset, so the subscription-aware
	// default still applies rather than becoming a bogus tier.
	if got := ResolveCacheTier("bogus", true, true); got != CacheTierLong {
		t.Fatalf("unknown tier on subscription = %q, want long default", got)
	}
	if got := ResolveCacheTier("bogus", false, true); got != CacheTierShort {
		t.Fatalf("unknown tier off subscription = %q, want short default", got)
	}
}

func TestObservedCacheTierInfersFromUsageAndHint(t *testing.T) {
	tests := []struct {
		name   string
		cached int
		write  int
		hint   string
		want   CacheTier
	}{
		{"long write", 0, 120, "1h", CacheTierLong},
		{"long write by name", 0, 120, "long", CacheTierLong},
		{"short write", 0, 80, "5m", CacheTierShort},
		{"pure long hit", 200, 0, "1h", CacheTierLong},
		{"long hit and write", 200, 30, "24h", CacheTierLong},
		{"short hit and write", 200, 30, "30m", CacheTierShort},
		{"no activity", 0, 0, "1h", CacheTierNone},
		{"unrecognized hint", 0, 120, "bogus", CacheTierNone},
		{"opt-out hint", 0, 120, "none", CacheTierNone},
		{"empty hint", 0, 120, "", CacheTierNone},
		{"negative activity", -1, 0, "1h", CacheTierNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ObservedCacheTier(tt.cached, tt.write, tt.hint); got != tt.want {
				t.Fatalf("ObservedCacheTier(%d, %d, %q) = %q, want %q", tt.cached, tt.write, tt.hint, got, tt.want)
			}
		})
	}
}

func TestObservedCacheTierZeroValueIsNone(t *testing.T) {
	if got := ObservedCacheTier(0, 0, ""); got != CacheTierNone {
		t.Fatalf("zero-value observation = %q, want none", got)
	}
}

func TestCacheTierCJKIndependent(t *testing.T) {
	// Non-ASCII retention text is not a recognized tier: it neither resolves
	// to a tier nor is mistaken for one, so both functions stay locale-neutral.
	if got := ResolveCacheTier("长时间", true, true); got != CacheTierLong {
		t.Fatalf("CJK config tier = %q, want subscription default long", got)
	}
	if got := ResolveCacheTier("短期", false, true); got != CacheTierShort {
		t.Fatalf("CJK config tier = %q, want short default", got)
	}
	if got := ObservedCacheTier(0, 120, "缓存一小时"); got != CacheTierNone {
		t.Fatalf("CJK ttl hint = %q, want none", got)
	}
	// Recognized ASCII tiers are unaffected by the presence of CJK elsewhere.
	if got := ObservedCacheTier(0, 120, " 1h "); got != CacheTierLong {
		t.Fatalf("ASCII tier with surrounding space = %q, want long", got)
	}
}
