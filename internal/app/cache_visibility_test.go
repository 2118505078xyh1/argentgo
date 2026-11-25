package app

import (
	"context"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"example.com/argentgo/internal/llm"
)

// cacheVisibilityFakeChatter 同时实现 Chatter 与 PromptCacheCapable，
// 用于驱动 CacheHintCapability 的各条分支；capability 字段可按用例设置。
type cacheVisibilityFakeChatter struct {
	capability llm.CacheCapability
}

func (c *cacheVisibilityFakeChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	return llm.Message{}, nil
}

func (c *cacheVisibilityFakeChatter) PromptCacheCapability() llm.CacheCapability {
	return c.capability
}

// cacheVisibilityPlainChatter 只实现 Chatter，不声明缓存能力，
// 用于验证 CacheSupport 的默认降级路径。
type cacheVisibilityPlainChatter struct{}

func (c *cacheVisibilityPlainChatter) Chat(_ context.Context, _ string, _ []llm.Message, _ []llm.ToolSpec, _ func(string)) (llm.Message, error) {
	return llm.Message{}, nil
}

// TestCacheStatsBoundaries 覆盖命中率的各个边界：全零、部分命中、全命中、
// 超额命中钳制、负读取、以及分母不可用（零/负）的情形。
// FreshTokens 的期望值直接按 llm.Usage.FreshPromptTokens 的口径给出。
func TestCacheStatsBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    llm.Usage
		want CacheVisibility
	}{
		{
			name: "all zero",
			u:    llm.Usage{},
			want: CacheVisibility{PromptTokens: 0, CachedTokens: 0, FreshTokens: 0, HitPercent: 0},
		},
		{
			name: "partial hit",
			u:    llm.Usage{PromptTokens: 100, CachedTokens: 30},
			want: CacheVisibility{PromptTokens: 100, CachedTokens: 30, FreshTokens: 70, HitPercent: 30},
		},
		{
			name: "full hit",
			u:    llm.Usage{PromptTokens: 100, CachedTokens: 100},
			want: CacheVisibility{PromptTokens: 100, CachedTokens: 100, FreshTokens: 0, HitPercent: 100},
		},
		{
			name: "over cached clamps to 100",
			u:    llm.Usage{PromptTokens: 100, CachedTokens: 150},
			want: CacheVisibility{PromptTokens: 100, CachedTokens: 150, FreshTokens: 0, HitPercent: 100},
		},
		{
			name: "negative cached yields zero percent",
			u:    llm.Usage{PromptTokens: 100, CachedTokens: -5},
			want: CacheVisibility{PromptTokens: 100, CachedTokens: -5, FreshTokens: 105, HitPercent: 0},
		},
		{
			name: "zero prompt denominator yields zero percent",
			u:    llm.Usage{PromptTokens: 0, CachedTokens: 5},
			want: CacheVisibility{PromptTokens: 0, CachedTokens: 5, FreshTokens: 0, HitPercent: 0},
		},
		{
			name: "negative prompt denominator yields zero percent",
			u:    llm.Usage{PromptTokens: -10, CachedTokens: 5},
			want: CacheVisibility{PromptTokens: -10, CachedTokens: 5, FreshTokens: 0, HitPercent: 0},
		},
		{
			name: "rounds to nearest integer",
			u:    llm.Usage{PromptTokens: 3, CachedTokens: 2},
			want: CacheVisibility{PromptTokens: 3, CachedTokens: 2, FreshTokens: 1, HitPercent: 67},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CacheStats(tc.u); got != tc.want {
				t.Fatalf("CacheStats(%+v) = %+v, want %+v", tc.u, got, tc.want)
			}
		})
	}
}

// TestCacheStatsDoesNotMutateInput 保证缓存统计是纯函数，入参 Usage 在调用前后
// 逐字段不变（Usage 按值传入且只含整数，天然无法被写回）。
func TestCacheStatsDoesNotMutateInput(t *testing.T) {
	u := llm.Usage{PromptTokens: 1000, CompletionTokens: 200, CachedTokens: 250, CacheWriteTokens: 50}
	before := u

	_ = CacheStats(u)
	_ = CacheStats(u)

	if u != before {
		t.Fatalf("CacheStats mutated its input: before %+v, after %+v", before, u)
	}
}

// TestCacheStatsDeterministic 保证同一入参多次调用结果逐字节一致，
// 与其他调用之间的交错无关（无隐藏状态、无时钟依赖）。
func TestCacheStatsDeterministic(t *testing.T) {
	u := llm.Usage{PromptTokens: 777, CachedTokens: 311}
	first := CacheStats(u)

	if got := CacheStats(u); got != first {
		t.Fatalf("first call = %+v, second call = %+v", first, got)
	}
	// 交错一组不同入参后再次调用同一入参，结果仍须一致。
	_ = CacheStats(llm.Usage{PromptTokens: math.MaxInt, CachedTokens: math.MaxInt})
	_ = CacheStats(llm.Usage{})
	if got := CacheStats(u); got != first {
		t.Fatalf("result changed after interleaving: first %+v, got %+v", first, got)
	}
}

// TestCacheStatsExtremeInputsBounded 对极值入参验证命中率始终落在 0..100，
// float64 路径不产生溢出或越界值；同时验证无字符串输入，结果与 CJK 无关。
func TestCacheStatsExtremeInputsBounded(t *testing.T) {
	for _, u := range []llm.Usage{
		{PromptTokens: math.MaxInt, CachedTokens: math.MaxInt},
		{PromptTokens: math.MaxInt, CachedTokens: 1},
		{PromptTokens: 1, CachedTokens: math.MaxInt},
		{PromptTokens: math.MinInt, CachedTokens: math.MaxInt},
		{PromptTokens: 10, CachedTokens: math.MinInt},
	} {
		got := CacheStats(u)
		if got.HitPercent < 0 || got.HitPercent > 100 {
			t.Errorf("CacheStats(%+v).HitPercent = %d, want 0..100", u, got.HitPercent)
		}
	}
}

// TestCacheHintCapabilityBranches 覆盖能力提示的各条分支及判定优先级：
// 已启用优先于本地端点与协议；本地端点优先于协议需显式开启的提示。
func TestCacheHintCapabilityBranches(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    llm.Chatter
		want string
	}{
		{"nil chatter", nil, cacheHintUnknown},
		{"plain chatter without capability", &cacheVisibilityPlainChatter{}, cacheHintUnknown},
		{"zero capability", &cacheVisibilityFakeChatter{}, cacheHintUnknown},
		{
			"supported",
			&cacheVisibilityFakeChatter{capability: llm.CacheCapability{Supported: true, Protocol: llm.ProtocolChatCompletions}},
			cacheHintEnabled,
		},
		{
			"local endpoint",
			&cacheVisibilityFakeChatter{capability: llm.CacheCapability{Local: true, Protocol: llm.ProtocolChatCompletions}},
			cacheHintLocal,
		},
		{
			"openai chat completions needs opt in",
			&cacheVisibilityFakeChatter{capability: llm.CacheCapability{Protocol: llm.ProtocolChatCompletions}},
			cacheHintOpenAINeedOptIn,
		},
		{
			"anthropic not enabled",
			&cacheVisibilityFakeChatter{capability: llm.CacheCapability{Protocol: llm.ProtocolAnthropicMessages}},
			cacheHintNotEnabled,
		},
		{
			"responses not enabled",
			&cacheVisibilityFakeChatter{capability: llm.CacheCapability{Protocol: llm.ProtocolResponses}},
			cacheHintNotEnabled,
		},
		{
			"supported wins over local and protocol",
			&cacheVisibilityFakeChatter{capability: llm.CacheCapability{Supported: true, Local: true, Protocol: llm.ProtocolChatCompletions}},
			cacheHintEnabled,
		},
		{
			"local wins over protocol",
			&cacheVisibilityFakeChatter{capability: llm.CacheCapability{Local: true, Protocol: llm.ProtocolAnthropicMessages}},
			cacheHintLocal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CacheHintCapability(tc.c); got != tc.want {
				t.Fatalf("CacheHintCapability(%T) = %q, want %q", tc.c, got, tc.want)
			}
		})
	}
}

// TestCacheHintCapabilityDoesNotMutate 保证提示生成不会改写 Chatter 上持有的能力状态。
func TestCacheHintCapabilityDoesNotMutate(t *testing.T) {
	fake := &cacheVisibilityFakeChatter{capability: llm.CacheCapability{Protocol: llm.ProtocolChatCompletions}}
	before := fake.capability

	_ = CacheHintCapability(fake)
	_ = CacheHintCapability(fake)

	if fake.capability != before {
		t.Fatalf("CacheHintCapability mutated its input: before %+v, after %+v", before, fake.capability)
	}
}

// TestCacheHintCapabilityCJKSafe 保证所有能力提示都是合法 UTF-8 中文，
// 固定分支不会因排版产生半个汉字或替换字符。
func TestCacheHintCapabilityCJKSafe(t *testing.T) {
	for _, hint := range []string{
		cacheHintEnabled,
		cacheHintLocal,
		cacheHintOpenAINeedOptIn,
		cacheHintNotEnabled,
		cacheHintUnknown,
	} {
		if !utf8.ValidString(hint) || utf8.RuneCountInString(hint) == 0 {
			t.Errorf("能力提示不是合法非空 UTF-8：%q", hint)
		}
	}
}

// TestCacheHintTextExact 锁定摘要文案的精确字节，包括中文标签与半角标点。
func TestCacheHintTextExact(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    CacheVisibility
		want string
	}{
		{
			name: "partial hit",
			v:    CacheVisibility{PromptTokens: 100, CachedTokens: 30, FreshTokens: 70, HitPercent: 30},
			want: "prompt cache 命中 30% (cached 30/100)",
		},
		{
			name: "full hit",
			v:    CacheVisibility{PromptTokens: 100, CachedTokens: 100, FreshTokens: 0, HitPercent: 100},
			want: "prompt cache 命中 100% (cached 100/100)",
		},
		{
			name: "missing denominator",
			v:    CacheVisibility{PromptTokens: 0, CachedTokens: 0, FreshTokens: 0, HitPercent: 0},
			want: cacheHintStatsUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CacheHintText(tc.v); got != tc.want {
				t.Fatalf("CacheHintText(%+v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

// TestCacheHintTextStableAndCJKSafe 保证摘要生成是纯函数：逐字节稳定，
// 输出为合法 UTF-8、含预期中文标签且无替换字符，不按字节截断。
func TestCacheHintTextStableAndCJKSafe(t *testing.T) {
	v := CacheVisibility{PromptTokens: 100, CachedTokens: 30, FreshTokens: 70, HitPercent: 30}
	first := CacheHintText(v)
	for i := 0; i < 3; i++ {
		if got := CacheHintText(v); got != first {
			t.Fatalf("CacheHintText 不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
		}
	}
	if !utf8.ValidString(first) {
		t.Fatalf("摘要不是合法 UTF-8：%q", first)
	}
	for _, r := range first {
		if r == utf8.RuneError {
			t.Fatalf("摘要含替换字符：%q", first)
		}
	}
	if want := "命中"; !strings.Contains(first, want) {
		t.Fatalf("摘要缺少中文标签 %q：%q", want, first)
	}
}
