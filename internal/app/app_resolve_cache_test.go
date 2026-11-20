package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/config"
	"github.com/2118505078xyh1/argentgo/internal/llm"
)

// assertCacheOptions 比较接线可能设置的缓存开关。llm.Options
// 带有 func 字段，因此不能用 == 比较。
func assertCacheOptions(t *testing.T, got, want llm.Options) {
	t.Helper()
	if got.NoPromptCache != want.NoPromptCache || got.CacheRetention != want.CacheRetention {
		t.Fatalf("cache options = {NoPromptCache:%v CacheRetention:%q}, want {NoPromptCache:%v CacheRetention:%q}",
			got.NoPromptCache, got.CacheRetention, want.NoPromptCache, want.CacheRetention)
	}
}

func TestResolveCacheOptionsUnsetKeepsExistingOptions(t *testing.T) {
	// 生产接线使用的保守事实集把未设置的层级解析为 short，
	// 这必须精确产生旧的字面量——
	// llm.Options{NoPromptCache: opts.NoPromptCache}——且无保留提示。
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
	// 当不支持长期保留时，显式的长效层级会被降级为 short：
	// 不得有 1h 提示到达线上。
	for _, tier := range []string{"long", "1h", "24h"} {
		got := resolveCacheOptions(tier, false, false, false)
		if got.CacheRetention != "" {
			t.Fatalf("resolveCacheOptions(%q, supportsLong=false).CacheRetention = %q, want empty", tier, got.CacheRetention)
		}
	}
	// 有支持时，同一层级会发出该提示。
	if got := resolveCacheOptions("long", false, true, false); got.CacheRetention != "1h" {
		t.Fatalf("resolveCacheOptions(\"long\", supportsLong=true).CacheRetention = %q, want 1h", got.CacheRetention)
	}
	// 订阅默认值遵循同样的门控。
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
