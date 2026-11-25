package bash

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"example.com/argentgo/internal/jobs"
	"example.com/argentgo/internal/pluginapi"
)

// 这些测试把原先留在 internal/tools 的运行时 helper 守卫补到 shell 侧：
// 生产实现逐字节迁移自 tools，因此这里断言的行为就是 tools 侧的行为。
// clipTo 相关用例全部走公开行为（不直接调字素函数），即使日后字素实现
// 下沉到 textutil 也仍然成立。

// shellClipMarker 是 clipTo 在保留的头尾之间插入的分隔符（与 tools 侧一致）。
const shellClipMarker = "\n... [truncated] ...\n"

func TestClampTimeout(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		raw       time.Duration
		globalMax time.Duration
		want      time.Duration
	}{
		// 各工具的默认值：raw<=0 回退到 toolTimeouts 的 Def。
		{"bash default", "bash", 0, 0, 120 * time.Second},
		{"bash negative default", "bash", -time.Second, 0, 120 * time.Second},
		{"fetch default", "fetch", 0, 0, 20 * time.Second},
		{"web_search default", "web_search", 0, 0, 20 * time.Second},
		{"git_status default", "git_status", 0, 0, 20 * time.Second},

		// 显式值、每个工具的上下界。
		{"explicit value within range", "bash", 30 * time.Second, 0, 30 * time.Second},
		{"bash over per-tool max", "bash", 86400 * time.Second, 0, 3600 * time.Second},
		{"fetch over per-tool max", "fetch", 5 * time.Minute, 0, 45 * time.Second},
		{"git_status over per-tool max", "git_status", 5 * time.Minute, 0, 60 * time.Second},
		{"min clamp", "bash", 100 * time.Millisecond, 0, time.Second},

		// globalMax 在默认值和显式值之后、min/max 之前钳制。
		{"global cap on default", "bash", 0, 10 * time.Second, 10 * time.Second},
		{"global cap on explicit value", "bash", 600 * time.Second, 30 * time.Second, 30 * time.Second},
		{"min clamp after global cap", "fetch", 0, 200 * time.Millisecond, time.Second},
		{"non-positive global cap ignored", "bash", 30 * time.Second, -time.Second, 30 * time.Second},
		{"zero global cap ignored", "bash", 30 * time.Second, 0, 30 * time.Second},

		// 未知工具：toolTimeouts 无条目时 raw 原样返回（连负值也不回退），
		// 但 globalMax 仍在 !ok 分支之前生效。
		{"unknown tool keeps raw", "read", 5 * time.Second, 0, 5 * time.Second},
		{"unknown tool keeps non-positive raw", "read", -5 * time.Second, 0, -5 * time.Second},
		{"unknown tool still capped globally", "read", 5 * time.Second, 2 * time.Second, 2 * time.Second},
		{"unknown tool ignores global cap when below", "read", time.Second, 5 * time.Second, time.Second},
		// CJK 工具名只是未知工具名，不改变钳制逻辑。
		{"CJK tool name is unknown", "中文工具", 7 * time.Second, 0, 7 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampTimeout(tt.tool, tt.raw, tt.globalMax); got != tt.want {
				t.Fatalf("clampTimeout(%q, %v, %v) = %v, want %v",
					tt.tool, tt.raw, tt.globalMax, got, tt.want)
			}
		})
	}
}

func TestOutputLimit(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want int
	}{
		{"injected positive", pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 512}), 512},
		{"injected one", pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 1}), 1},
		{"injected zero falls back", pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 0}), maxToolOutput},
		{"injected negative falls back", pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: -7}), maxToolOutput},
		{"not injected falls back", context.Background(), maxToolOutput},
		{"different context value ignored", context.WithValue(context.Background(), struct{}{}, 7), maxToolOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outputLimit(tt.ctx); got != tt.want {
				t.Fatalf("outputLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestMaxTimeout(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want time.Duration
	}{
		{
			name: "injected",
			ctx:  pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxTimeout: 3 * time.Second}),
			want: 3 * time.Second,
		},
		{
			name: "not injected",
			ctx:  context.Background(),
			want: 0,
		},
		{
			name: "zero injected",
			ctx:  pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{}),
			want: 0,
		},
		// shell 侧的 maxTimeout 只是读取注入值，不做 tools 侧的正数校验。
		{
			name: "negative passes through",
			ctx:  pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxTimeout: -time.Second}),
			want: -time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxTimeout(tt.ctx); got != tt.want {
				t.Fatalf("maxTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJobRegistry(t *testing.T) {
	real, err := jobs.New(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	var typedNil *jobs.Registry

	tests := []struct {
		name string
		ctx  context.Context
		want *jobs.Registry
	}{
		{
			name: "injected registry",
			ctx:  pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{Jobs: real}),
			want: real,
		},
		{
			// 带类型的 nil 能成功断言，返回的是 nil 指针。
			name: "typed nil registry",
			ctx:  pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{Jobs: typedNil}),
			want: nil,
		},
		{
			name: "wrong type",
			ctx:  pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{Jobs: "not a registry"}),
			want: nil,
		},
		{
			name: "nil jobs field",
			ctx:  pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{}),
			want: nil,
		},
		{
			name: "not injected",
			ctx:  context.Background(),
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := jobRegistry(tt.ctx)
			if got != tt.want {
				t.Fatalf("jobRegistry() = %p, want %p", got, tt.want)
			}
			// 注入真实 registry 时应当拿回同一个实例，而不是副本。
			if tt.want == real && got.Dir() != real.Dir() {
				t.Fatalf("jobRegistry() = %p, want the injected registry %p", got, real)
			}
		})
	}
}

// splitShellClip 把截断结果拆成头/尾，并报告是否出现 marker。
func splitShellClip(t *testing.T, got string) (head, tail string, truncated bool) {
	t.Helper()
	if !strings.Contains(got, shellClipMarker) {
		return got, "", false
	}
	parts := strings.Split(got, shellClipMarker)
	if len(parts) != 2 {
		t.Fatalf("clipped result has %d markers: %q", len(parts)-1, got)
	}
	return parts[0], parts[1], true
}

// isClusterExtendRune 报告 r 是附着在基础 rune 之后的标记：组合符、
// ZWJ/ZWNJ、变体符、肤色与标签字符。它们不能出现在保留尾部的开头。
func isClusterExtendRune(r rune) bool {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
		return true
	}
	switch {
	case r == 0x200D, r == 0x200C, // ZWJ / ZWNJ
		r >= 0xFE00 && r <= 0xFE0F,   // 变体选择符
		r >= 0x1F3FB && r <= 0x1F3FF, // emoji 肤色修饰符
		r >= 0xE0020 && r <= 0xE007F: // emoji 标签字符
		return true
	}
	return false
}

// isDanglingConnector 报告 r 是一个悬空的连接符：ZWJ/ZWNJ 必须连接
// 前后两个 rune，因此不能作为保留头部的最后一个 rune。组合符、变体符
// 等附着在已有基础 rune 之后，作为头部结尾是完整字簇，属于正常情况。
func isDanglingConnector(r rune) bool { return r == 0x200D || r == 0x200C }

func isRegionalIndicatorRune(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func firstRuneOf(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	return r
}

func lastRuneOf(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// assertShellClip 复刻 tools 侧的核心不变量：UTF-8 有效、字节/rune 预算、
// marker 恰好一次、字素簇不悬挂。非法输入本身不是合法 UTF-8 时跳过有效性断言。
func assertShellClip(t *testing.T, text string, limit int, got string) (head, tail string, truncated bool) {
	t.Helper()
	budget := limit
	if budget <= 0 {
		budget = maxToolOutput
	}
	head, tail, truncated = splitShellClip(t, got)

	if len(text) <= budget {
		if truncated || got != text {
			t.Fatalf("clipTo(%q, %d) = %q, want unchanged %q", text, limit, got, text)
		}
		return
	}
	if !truncated {
		t.Fatalf("clipTo(%q, %d) = %q, expected truncation marker", text, limit, got)
	}
	if n := strings.Count(got, shellClipMarker); n != 1 {
		t.Fatalf("clipTo(%q, %d) has %d truncation markers, want 1", text, limit, n)
	}
	if utf8.ValidString(text) && !utf8.ValidString(got) {
		t.Fatalf("clipTo(%q, %d) produced invalid UTF-8: %q", text, limit, got)
	}
	if runes := utf8.RuneCountInString(head) + utf8.RuneCountInString(tail); runes > budget {
		t.Fatalf("clipTo(%q, %d) kept %d runes, budget is %d", text, limit, runes, budget)
	}
	if bytes := len(head) + len(tail); bytes > budget {
		t.Fatalf("clipTo(%q, %d) kept %d bytes, budget is %d", text, limit, bytes, budget)
	}

	// 切分点不能把字素簇劈开：头不以悬空连接符/半面旗帜结尾，
	// 尾不以附着符开头，不切断 CRLF 或旗帜。
	if head != "" {
		if r := lastRuneOf(head); isDanglingConnector(r) || isRegionalIndicatorRune(r) && isRegionalIndicatorRune(firstRuneOf(tail)) {
			t.Fatalf("clipTo(%q, %d) ends head with a dangling cluster: head=%q tail=%q", text, limit, head, tail)
		}
	}
	if tail != "" && isClusterExtendRune(firstRuneOf(tail)) {
		t.Fatalf("clipTo(%q, %d) starts tail with a dangling cluster: head=%q tail=%q", text, limit, head, tail)
	}
	if strings.HasSuffix(head, "\r") && strings.HasPrefix(tail, "\n") {
		t.Fatalf("clipTo(%q, %d) split a CRLF pair", text, limit)
	}
	return
}

func TestClipToPassthrough(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
	}{
		{"empty", "", 8},
		{"shorter than limit", "abc", 10},
		{"exactly at limit", "abcdefghij", 10},
		{"one byte over", "abcdefghijk", 10},
		{"zero limit uses default", "短文本😀", 0},
		{"negative limit uses default", strings.Repeat("中", 5), -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertShellClip(t, tc.text, tc.limit, clipTo(tc.text, tc.limit))
		})
	}
}

// TestClipToHeadTail 用 ASCII 精确断言头尾字节数与 marker 位置，
// 与 tools 侧 clipTo 的 head=limit*2/3、tail=limit-head 行为对照。
func TestClipToHeadTail(t *testing.T) {
	const text = "abcdefghijklmnopqrstuvwxyz"
	cases := []struct {
		name  string
		limit int
		want  string
	}{
		{"limit one", 1, shellClipMarker + "z"},
		{"limit two", 2, "a" + shellClipMarker + "z"},
		{"limit nine", 9, "abcdef" + shellClipMarker + "xyz"},
		{"limit ten", 10, "abcdef" + shellClipMarker + "wxyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clipTo(text, tc.limit)
			assertShellClip(t, text, tc.limit, got)
			if got != tc.want {
				t.Fatalf("clipTo(text, %d) = %q, want %q", tc.limit, got, tc.want)
			}
		})
	}
}

// TestClipToGraphemeBoundaries 覆盖最容易切坏的多 rune 字素簇：
// ZWJ emoji、区域指示符旗帜、组合符、CRLF 与非法字节。
func TestClipToGraphemeBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		limit int
		want  string // 为空时只跑不变量断言
	}{
		{
			name:  "ZWJ family",
			text:  strings.Repeat("a👨‍👩‍👧", 4),
			limit: 11,
		},
		{
			name:  "regional indicator flags",
			text:  strings.Repeat("🇨🇳", 4) + "end",
			limit: 12,
		},
		{
			name:  "combining marks",
			text:  strings.Repeat("e\u0301", 6),
			limit: 9,
			want:  "e\u0301e\u0301" + shellClipMarker + "e\u0301",
		},
		{
			name:  "CRLF pair",
			text:  "abc\r\ndefghij",
			limit: 6,
			want:  "abc" + shellClipMarker + "ij",
		},
		{
			name:  "invalid bytes preserved",
			text:  "ab\xff\xfe中cd",
			limit: 5,
			want:  "ab\xff" + shellClipMarker + "cd",
		},
		{
			name:  "CJK cut inside rune",
			text:  strings.Repeat("中文测试", 2),
			limit: 7,
			want:  "中" + shellClipMarker + "试",
		},
		{
			name:  "tail boundary mid emoji",
			text:  "中中😀中中😀",
			limit: 9,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clipTo(tc.text, tc.limit)
			assertShellClip(t, tc.text, tc.limit, got)
			if tc.want != "" && got != tc.want {
				t.Fatalf("clipTo(%q, %d) = %q, want %q", tc.text, tc.limit, got, tc.want)
			}
			// 落在保留头部里的非法字节原样留下，不能被替换成 U+FFFD。
			// 落在被丢弃中段的字节则应随中段一起消失。
			if tc.name == "invalid bytes preserved" {
				if strings.Count(got, "\xff") != 1 || strings.Contains(got, "\xef\xbf\xbd") {
					t.Fatalf("clipTo(%q, %d) did not preserve invalid bytes: %q", tc.text, tc.limit, got)
				}
			}
		})
	}
}

// TestClipToStaysValidAcrossLimits 遍历每段文本长度附近的所有 limit，
// 让头尾偏移落在 rune 内每个可能的字节位置（与 tools 侧差分思路一致）。
func TestClipToStaysValidAcrossLimits(t *testing.T) {
	texts := []string{
		"abcdefghij",
		strings.Repeat("中文", 9),
		strings.Repeat("😀", 9),
		strings.Repeat("ab中😀e\u0301", 4),
		"中中😀中中😀",
		"a👨‍👩‍👧b",
		"🇨🇳ab🇨🇳",
	}
	for _, text := range texts {
		for limit := 0; limit <= len(text)+2; limit++ {
			assertShellClip(t, text, limit, clipTo(text, limit))
		}
	}
}

// trimPartialRune 与字素簇边界函数已下沉到 internal/textutil，由该包负责
// 自身单测；这里只通过 clipTo 的对外行为守卫 shell 侧的裁剪结果。

func TestClipUsesInjectedOutputLimit(t *testing.T) {
	const text = "abcdefghijklmnopqrstuvwxyz"
	ctx := pluginapi.WithExecContext(context.Background(), pluginapi.ExecContext{MaxOutput: 9})
	got := clip(ctx, text)
	assertShellClip(t, text, 9, got)
	if want := "abcdef" + shellClipMarker + "xyz"; got != want {
		t.Fatalf("clip(ctx, text) = %q, want %q", got, want)
	}
}
