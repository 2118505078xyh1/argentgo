package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// assertCacheOptions compares the cache knobs the wiring may set. llm.Options
// carries a func field, so it cannot be compared with ==.
func assertCacheOptions(t *testing.T, got, want llm.Options) {
	t.Helper()
	if got.NoPromptCache != want.NoPromptCache || got.CacheRetention != want.CacheRetention {
		t.Fatalf("cache options = {NoPromptCache:%v CacheRetention:%q}, want {NoPromptCache:%v CacheRetention:%q}",
			got.NoPromptCache, got.CacheRetention, want.NoPromptCache, want.CacheRetention)
	}
}

func TestResolveCacheOptionsUnsetKeepsExistingOptions(t *testing.T) {
	// The conservative fact set used by the production wiring resolves an
	// unset tier to short, which must produce exactly the old literal —
	// llm.Options{NoPromptCache: opts.NoPromptCache} — with no retention hint.
	if got := ResolveCacheTier("", false, true); got != CacheTierShort {
		t.Fatalf("ResolveCacheTier(\"\", false, true) = %q, want short", got)
	}
	for _, noCache := range []bool{false, true} {
		got := resolveCacheOptions("", false, true, noCache)
		assertCacheOptions(t, got, llm.Options{NoPromptCache: noCache})
	}
}

func TestResolveCacheOptionsExplicitTierTakesEffect(t *testing.T) {
	tests := []struct {
		name          string
		configTier    string
		wantRetention string
		wantNoPrompt  bool
	}{
		{"long by name", "long", "1h", false},
		{"one hour hint", "1h", "1h", false},
		{"24 hour hint", "24h", "1h", false},
		{"short by name", "short", "", false},
		{"five minute hint", "5m", "", false},
		{"thirty minute hint", "30m", "", false},
		{"none opt-out", "none", "", true},
		{"off opt-out", "off", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveCacheOptions(tt.configTier, false, true, false)
			if got.CacheRetention != tt.wantRetention || got.NoPromptCache != tt.wantNoPrompt {
				t.Fatalf("resolveCacheOptions(%q) = %+v, want retention %q / noCache %v",
					tt.configTier, got, tt.wantRetention, tt.wantNoPrompt)
			}
		})
	}
}

func TestResolveCacheOptionsLongDowngradesWithoutSupport(t *testing.T) {
	// An explicit long tier is downgraded to short when long retention is
	// unavailable: no 1h hint may reach the wire.
	for _, tier := range []string{"long", "1h", "24h"} {
		got := resolveCacheOptions(tier, false, false, false)
		if got.CacheRetention != "" {
			t.Fatalf("resolveCacheOptions(%q, supportsLong=false).CacheRetention = %q, want empty", tier, got.CacheRetention)
		}
	}
	// With support present the same tier emits the hint.
	if got := resolveCacheOptions("long", false, true, false); got.CacheRetention != "1h" {
		t.Fatalf("resolveCacheOptions(\"long\", supportsLong=true).CacheRetention = %q, want 1h", got.CacheRetention)
	}
	// The subscription default follows the same gating.
	if got := resolveCacheOptions("", true, true, false); got.CacheRetention != "1h" {
		t.Fatalf("subscription default with support = %+v, want 1h", got)
	}
	if got := resolveCacheOptions("", true, false, false); got.CacheRetention != "" {
		t.Fatalf("subscription default without support = %+v, want no hint", got)
	}
}

func TestCacheOptionsForConfigWiresExplicitTier(t *testing.T) {
	tests := []struct {
		name          string
		raw           map[string]any
		noPromptCache bool
		want          llm.Options
	}{
		{
			name: "unset tier keeps default",
			raw:  map[string]any{},
			want: llm.Options{},
		},
		{
			name:          "unset tier keeps CLI opt-out",
			raw:           map[string]any{},
			noPromptCache: true,
			want:          llm.Options{NoPromptCache: true},
		},
		{
			name: "camel case long config",
			raw:  map[string]any{"cacheTier": "long"},
			want: llm.Options{CacheRetention: "1h"},
		},
		{
			name: "kebab case long config",
			raw:  map[string]any{"cache-tier": "1h"},
			want: llm.Options{CacheRetention: "1h"},
		},
		{
			name: "prompt cache tier alias",
			raw:  map[string]any{"promptCacheTier": "long"},
			want: llm.Options{CacheRetention: "1h"},
		},
		{
			name: "explicit none config",
			raw:  map[string]any{"cacheTier": "none"},
			want: llm.Options{NoPromptCache: true},
		},
		{
			name:          "CLI opt-out wins over long config",
			raw:           map[string]any{"cacheTier": "long"},
			noPromptCache: true,
			want:          llm.Options{CacheRetention: "1h", NoPromptCache: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Raw: tt.raw}
			got := cacheOptionsForConfig(cfg, cli.Options{NoPromptCache: tt.noPromptCache})
			assertCacheOptions(t, got, tt.want)
		})
	}
}
